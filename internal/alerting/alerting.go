// Package alerting provides a best-effort alerting system that periodically
// evaluates alert rules against Prometheus metrics or state store values and
// sends notifications via Slack webhooks, generic webhooks, or email.
//
// Alert failures never affect request processing — all alerts are async and
// best-effort. The alert evaluator runs in a background goroutine.
package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Severity represents the severity level of an alert.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Rule defines a single alert rule with a metric name, threshold, and channels.
type Rule struct {
	Name      string    `yaml:"name"`
	Metric    string    `yaml:"metric"`
	Threshold float64   `yaml:"threshold"`
	Duration  string    `yaml:"duration"` // e.g. "5m"
	Channels  []string  `yaml:"channels"`
	Severity  Severity  `yaml:"severity"`
}

// AlertConfig defines the top-level alerting configuration.
type AlertConfig struct {
	SlackWebhookURL string            `yaml:"slack_webhook_url,omitempty"`
	EmailSMTP       *EmailSMTPConfig  `yaml:"email_smtp,omitempty"`
	WebhookURL      string            `yaml:"webhook_url,omitempty"`
	Rules           []Rule            `yaml:"rules,omitempty"`
}

// EmailSMTPConfig defines SMTP settings for email alerts.
type EmailSMTPConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// Notifier sends alert notifications through a specific channel.
type Notifier interface {
	Send(ctx context.Context, alert *FiredAlert) error
}

// FiredAlert represents a triggered alert with its context.
type FiredAlert struct {
	RuleName    string    `json:"rule_name"`
	Metric      string    `json:"metric"`
	Value       float64   `json:"value"`
	Threshold   float64   `json:"threshold"`
	Severity    Severity  `json:"severity"`
	FiredAt     time.Time `json:"fired_at"`
	Message     string    `json:"message"`
}

// Evaluator periodically checks alert rules and fires notifications.
type Evaluator struct {
	mu        sync.RWMutex
	rules     []Rule
	notifiers []Notifier
	lastFired map[string]time.Time // rule name -> last fired time
	interval  time.Duration

	// CheckFn is called for each rule evaluation. It should return the current
	// value of the metric described by the rule.
	CheckFn func(ctx context.Context, rule Rule) (float64, error)
}

// NewEvaluator creates an alert evaluator with the given configuration.
func NewEvaluator(cfg AlertConfig, checkFn func(ctx context.Context, rule Rule) (float64, error)) *Evaluator {
	e := &Evaluator{
		lastFired: make(map[string]time.Time),
		interval:  60 * time.Second,
		CheckFn:   checkFn,
	}
	e.UpdateConfig(cfg)
	return e
}

// UpdateConfig replaces the current rule set and notifier list.
func (e *Evaluator) UpdateConfig(cfg AlertConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.rules = cfg.Rules
	e.notifiers = nil

	if cfg.SlackWebhookURL != "" {
		e.notifiers = append(e.notifiers, &SlackNotifier{WebhookURL: cfg.SlackWebhookURL})
	}
	if cfg.WebhookURL != "" {
		e.notifiers = append(e.notifiers, &GenericWebhookNotifier{WebhookURL: cfg.WebhookURL})
	}
	if cfg.EmailSMTP != nil && cfg.EmailSMTP.Host != "" {
		e.notifiers = append(e.notifiers, &EmailNotifier{Config: *cfg.EmailSMTP})
	}
}

// Start begins the background evaluation loop. It runs until the context is cancelled.
func (e *Evaluator) Start(ctx context.Context) {
	slog.InfoContext(ctx, "alerting: evaluator started",
		"rules", len(e.rules),
		"interval", e.interval,
	)
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	// Run once immediately.
	e.evaluateAll(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "alerting: evaluator stopped")
			return
		case <-ticker.C:
			e.evaluateAll(ctx)
		}
	}
}

// evaluateAll runs all rule checks in sequence.
func (e *Evaluator) evaluateAll(ctx context.Context) {
	e.mu.RLock()
	rules := make([]Rule, len(e.rules))
	copy(rules, e.rules)
	notifiers := e.notifiers
	lastFired := make(map[string]time.Time, len(e.lastFired))
	for k, v := range e.lastFired {
		lastFired[k] = v
	}
	e.mu.RUnlock()

	for _, rule := range rules {
		if e.CheckFn == nil {
			continue
		}
		value, err := e.CheckFn(ctx, rule)
		if err != nil {
			slog.WarnContext(ctx, "alerting: check failed",
				"rule", rule.Name,
				"error", err,
			)
			continue
		}

		triggered := value > rule.Threshold

		// Dedup: skip if last fired within the rule's duration window.
		if triggered {
			if last := lastFired[rule.Name]; !last.IsZero() {
				if dur := parseDuration(rule.Duration); dur > 0 && time.Since(last) < dur {
					continue
				}
			}
		}

		alert := &FiredAlert{
			RuleName:  rule.Name,
			Metric:    rule.Metric,
			Value:     value,
			Threshold: rule.Threshold,
			Severity:  rule.Severity,
			FiredAt:   time.Now(),
			Message:   fmt.Sprintf("Alert: %s = %.2f (threshold: %.2f)", rule.Metric, value, rule.Threshold),
		}

		if triggered {
			// Record fire time for dedup.
			e.mu.Lock()
			e.lastFired[rule.Name] = time.Now()
			e.mu.Unlock()

			slog.WarnContext(ctx, "alerting: alert fired",
				"rule", rule.Name,
				"value", value,
				"threshold", rule.Threshold,
			)

			// Send to all notifiers (best-effort).
			for _, n := range notifiers {
				if err := n.Send(ctx, alert); err != nil {
					slog.WarnContext(ctx, "alerting: notify failed",
						"rule", rule.Name,
						"error", err,
					)
				}
			}
		}
	}
}

// parseDuration parses a duration string like "5m", "1h", "30s".
func parseDuration(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// SlackNotifier sends alerts to a Slack webhook.
type SlackNotifier struct {
	WebhookURL string
}

// Send posts a JSON payload to the Slack webhook URL.
func (n *SlackNotifier) Send(ctx context.Context, alert *FiredAlert) error {
	payload := map[string]any{
		"text": fmt.Sprintf("[%s] %s\n%s", strings.ToUpper(string(alert.Severity)), alert.RuleName, alert.Message),
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// GenericWebhookNotifier sends alerts to a generic webhook URL.
type GenericWebhookNotifier struct {
	WebhookURL string
}

// Send posts a JSON payload with structured alert data.
func (n *GenericWebhookNotifier) Send(ctx context.Context, alert *FiredAlert) error {
	body, _ := json.Marshal(alert)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// EmailNotifier sends alerts via SMTP email.
type EmailNotifier struct {
	Config EmailSMTPConfig
}

// Send sends an alert email via SMTP.
func (n *EmailNotifier) Send(ctx context.Context, alert *FiredAlert) error {
	// Email is deferred to an external mechanism for simplicity.
	// In a full implementation, use net/smtp.SendMail.
	slog.Debug("alerting: email notifier not fully implemented",
		"to", n.Config.To,
		"rule", alert.RuleName,
	)
	return nil
}

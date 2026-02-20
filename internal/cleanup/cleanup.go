package cleanup

import (
	"context"
	"log/slog"
	"time"

	"github.com/Parsons-ai/comms-server/internal/store"
)

// Service runs periodic cleanup tasks: expired messages, delivered messages,
// storage tracking updates, and old call log pruning.
type Service struct {
	db       *store.DB
	logger   *slog.Logger
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// New creates a cleanup service that runs every interval.
func New(db *store.DB, interval time.Duration, logger *slog.Logger) *Service {
	return &Service{
		db:       db,
		logger:   logger.With("service", "cleanup"),
		interval: interval,
		done:     make(chan struct{}),
	}
}

// Name implements Service.
func (s *Service) Name() string { return "cleanup" }

// Start implements Service.
func (s *Service) Start(ctx context.Context) error {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.loop(ctx)
	s.logger.Info("cleanup service started", "interval", s.interval)
	return nil
}

// Stop implements Service.
func (s *Service) Stop(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
	return nil
}

// Health implements Service.
func (s *Service) Health() error {
	return s.db.Ping()
}

func (s *Service) loop(ctx context.Context) {
	defer close(s.done)

	// Run immediately on start
	s.runAll()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runAll()
		}
	}
}

func (s *Service) runAll() {
	s.cleanExpiredMessages()
	s.cleanDeliveredMessages()
	s.pruneOldCallLogs()
	s.updateStorageUsage()
}

// cleanExpiredMessages deletes messages past their expires_at timestamp.
func (s *Service) cleanExpiredMessages() {
	result, err := s.db.Exec("DELETE FROM messages WHERE expires_at IS NOT NULL AND expires_at < datetime('now')")
	if err != nil {
		s.logger.Error("clean expired messages", "error", err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		s.logger.Info("cleaned expired messages", "count", rows)
	}
}

// cleanDeliveredMessages deletes delivered messages older than 1 hour.
// Gives recipients a window to re-fetch if needed.
func (s *Service) cleanDeliveredMessages() {
	result, err := s.db.Exec("DELETE FROM messages WHERE delivered = 1 AND created_at < datetime('now', '-1 hour')")
	if err != nil {
		s.logger.Error("clean delivered messages", "error", err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		s.logger.Info("cleaned delivered messages", "count", rows)
	}
}

// pruneOldCallLogs deletes call log entries older than 90 days.
func (s *Service) pruneOldCallLogs() {
	result, err := s.db.Exec("DELETE FROM call_log WHERE started_at < datetime('now', '-90 days')")
	if err != nil {
		s.logger.Error("prune call logs", "error", err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		s.logger.Info("pruned old call logs", "count", rows)
	}
}

// updateStorageUsage recalculates storage usage per user per category.
func (s *Service) updateStorageUsage() {
	// Update message storage
	_, err := s.db.Exec(`
		INSERT INTO storage_usage (user_id, category, size_bytes, updated_at)
		SELECT recipient_id, 'messages', COALESCE(SUM(size_bytes), 0), datetime('now')
		FROM messages
		WHERE delivered = 0
		GROUP BY recipient_id
		ON CONFLICT(user_id, category) DO UPDATE SET
			size_bytes = excluded.size_bytes,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		s.logger.Error("update message storage", "error", err)
	}
}

// RunOnce executes all cleanup tasks once (useful for testing).
func (s *Service) RunOnce() {
	s.runAll()
}

package devseed

import (
	"context"
	"fmt"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type Event struct {
	Type    string
	Content string
}

type SessionSpec struct {
	Channel    string
	Orphaned   bool
	Thread     string
	TriggerSeq int64
	Agent      string
	Status     string
	ReplyMode  string
	Command    string
	Error      string
	ExitCode   int64
	Duration   time.Duration
	ReplyToSeq int64
	Events     []Event
}

func Stage(s *store.Store, specs []SessionSpec) ([]int64, error) {
	ctx := context.Background()
	ids := make([]int64, 0, len(specs))
	for _, spec := range specs {
		id, err := stage(ctx, s, spec)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func stage(ctx context.Context, s *store.Store, spec SessionSpec) (int64, error) {
	if spec.Status != store.SessionQueued &&
		spec.Status != store.SessionRunning &&
		!terminal(spec.Status) {
		return 0, fmt.Errorf("bad session status %q", spec.Status)
	}
	thread, err := resolveThread(ctx, s, spec)
	if err != nil {
		return 0, err
	}
	messages, err := s.ListMessages(ctx, thread.ID, 0)
	if err != nil {
		return 0, err
	}
	trigger, err := findSeq(messages, spec.TriggerSeq)
	if err != nil {
		return 0, fmt.Errorf("thread %q: %w", spec.Thread, err)
	}
	started, err := time.Parse(time.RFC3339, trigger.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("thread %q trigger seq %d: created_at %q: %w", spec.Thread, spec.TriggerSeq, trigger.CreatedAt, err)
	}
	startedAt := started.Add(time.Second)

	id, err := s.CreateSession(ctx, thread.ID, trigger.ID, spec.Agent, store.SessionQueued, spec.ReplyMode, spec.Command, nil)
	if err != nil {
		return 0, fmt.Errorf("thread %q: %w", spec.Thread, err)
	}
	if spec.Status != store.SessionQueued {
		if err := s.MarkSessionRunning(ctx, id, startedAt.Format(time.RFC3339)); err != nil {
			return 0, err
		}
	}
	if terminal(spec.Status) {
		finishedAt := startedAt.Add(spec.Duration)
		if err := s.FinishSession(ctx, id, spec.Status, exitPtr(spec), errorPtr(spec), finishedAt.Format(time.RFC3339)); err != nil {
			return 0, err
		}
	}
	if spec.ReplyToSeq > 0 {
		reply, err := findSeq(messages, spec.ReplyToSeq)
		if err != nil {
			return 0, fmt.Errorf("thread %q: %w", spec.Thread, err)
		}
		if err := s.SetSessionReply(ctx, id, reply.ID); err != nil {
			return 0, err
		}
	}
	for _, event := range spec.Events {
		if _, _, err := s.AppendSessionEvent(ctx, id, event.Type, event.Content); err != nil {
			return 0, fmt.Errorf("session %d: %w", id, err)
		}
	}
	return id, nil
}

func terminal(status string) bool {
	switch status {
	case store.SessionSucceeded, store.SessionFailed, store.SessionCanceled:
		return true
	}
	return false
}

func exitPtr(spec SessionSpec) *int64 {
	if spec.ExitCode == 0 && spec.Status != store.SessionFailed {
		return nil
	}
	code := spec.ExitCode
	return &code
}

func errorPtr(spec SessionSpec) *string {
	if spec.Error == "" {
		return nil
	}
	msg := spec.Error
	return &msg
}

func resolveThread(ctx context.Context, s *store.Store, spec SessionSpec) (store.Thread, error) {
	channels, err := s.ListChannels(ctx, "", spec.Orphaned)
	if err != nil {
		return store.Thread{}, err
	}
	for _, channel := range channels {
		if channel.Name != spec.Channel {
			continue
		}
		threads, err := s.ListThreads(ctx, channel.ID)
		if err != nil {
			return store.Thread{}, err
		}
		for _, thread := range threads {
			if thread.Title == spec.Thread {
				return thread, nil
			}
		}
	}
	return store.Thread{}, fmt.Errorf("thread %q not found in channel %q", spec.Thread, spec.Channel)
}

func findSeq(messages []store.Message, seq int64) (store.Message, error) {
	for _, message := range messages {
		if message.Seq == seq {
			return message, nil
		}
	}
	return store.Message{}, fmt.Errorf("no message at seq %d", seq)
}

package stt

import (
	"context"
	"fmt"
	"sync"

	"github.com/user/discord-notetaker/internal/audio"
	"github.com/rs/zerolog/log"
)

// Transcriber interface for STT backends
type Transcriber interface {
	Transcribe(ctx context.Context, chunk *audio.Chunk) ([]audio.Utterance, error)
	Close() error
}

// TranscriberPool manages a pool of STT workers
type TranscriberPool struct {
	transcriber    Transcriber
	workers        int
	chunkChan      chan *audio.Chunk
	utteranceChan  chan []audio.Utterance
	stopChan       chan struct{}
	wg             sync.WaitGroup
	started        bool
	mutex          sync.Mutex
}

func NewTranscriberPool(transcriber Transcriber, workers int) *TranscriberPool {
	return &TranscriberPool{
		transcriber:   transcriber,
		workers:       workers,
		chunkChan:     make(chan *audio.Chunk, workers*2),
		utteranceChan: make(chan []audio.Utterance, workers*2),
		stopChan:      make(chan struct{}),
	}
}

func (p *TranscriberPool) Start(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.started {
		return fmt.Errorf("pool already started")
	}

	p.started = true

	// Start worker goroutines
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}

	log.Info().Int("workers", p.workers).Msg("Started STT worker pool")
	return nil
}

func (p *TranscriberPool) worker(ctx context.Context, workerID int) {
	defer p.wg.Done()

	log.Debug().Int("worker_id", workerID).Msg("STT worker started")
	defer log.Debug().Int("worker_id", workerID).Msg("STT worker stopped")

	for {
		select {
		case chunk, ok := <-p.chunkChan:
			if !ok {
				return
			}

			utterances, err := p.transcriber.Transcribe(ctx, chunk)
			if err != nil {
				log.Error().
					Err(err).
					Str("chunk_id", chunk.ID.String()).
					Int("worker_id", workerID).
					Msg("Failed to transcribe chunk")
				continue
			}

			if len(utterances) > 0 {
				select {
				case p.utteranceChan <- utterances:
					log.Debug().
						Int("utterances", len(utterances)).
						Str("chunk_id", chunk.ID.String()).
						Int("worker_id", workerID).
						Msg("Transcribed chunk")
				case <-ctx.Done():
					return
				case <-p.stopChan:
					return
				}
			}

		case <-ctx.Done():
			return
		case <-p.stopChan:
			return
		}
	}
}

func (p *TranscriberPool) ProcessChunk(chunk *audio.Chunk) error {
	select {
	case p.chunkChan <- chunk:
		return nil
	default:
		return fmt.Errorf("chunk queue full, dropping chunk")
	}
}

func (p *TranscriberPool) GetUtterances() <-chan []audio.Utterance {
	return p.utteranceChan
}

func (p *TranscriberPool) Stop() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if !p.started {
		return
	}

	close(p.stopChan)
	close(p.chunkChan)

	// Wait for all workers to finish
	p.wg.Wait()
	close(p.utteranceChan)

	p.started = false
	log.Info().Msg("Stopped STT worker pool")
}
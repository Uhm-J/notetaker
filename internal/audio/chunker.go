package audio

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

type RingChunker struct {
	chunkSeconds   int
	overlapMS      int
	sampleRate     int
	chunkSamples   int
	overlapSamples int

	buffer        []int16
	timestamps    []time.Time
	speakers      [][]string
	bufferPos     int
	lastChunkTime time.Time

	chunkChan chan *Chunk
	stopChan  chan struct{}
	stopped   bool
	mutex     sync.RWMutex
}

func NewRingChunker(chunkSeconds, overlapMS, sampleRate int) *RingChunker {
	chunkSamples := chunkSeconds * sampleRate
	overlapSamples := (overlapMS * sampleRate) / 1000

	// Buffer needs to hold at least one chunk plus overlap
	bufferSize := chunkSamples + overlapSamples

	return &RingChunker{
		chunkSeconds:   chunkSeconds,
		overlapMS:      overlapMS,
		sampleRate:     sampleRate,
		chunkSamples:   chunkSamples,
		overlapSamples: overlapSamples,
		buffer:         make([]int16, bufferSize),
		timestamps:     make([]time.Time, bufferSize),
		speakers:       make([][]string, bufferSize),
		chunkChan:      make(chan *Chunk, 10),
		stopChan:       make(chan struct{}),
	}
}

func (c *RingChunker) AddSamples(pcm []int16, timestamp time.Time, speakers []string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.stopped {
		return
	}

	// Add samples to ring buffer
	for i, sample := range pcm {
		if c.bufferPos+i >= len(c.buffer) {
			// Buffer is full, create a chunk
			c.createChunk()
			c.slideBuffer()
		}

		pos := (c.bufferPos + i) % len(c.buffer)
		c.buffer[pos] = sample
		c.timestamps[pos] = timestamp.Add(time.Duration(i) * time.Second / time.Duration(c.sampleRate))
		c.speakers[pos] = speakers
	}

	c.bufferPos += len(pcm)

	// Check if it's time to create a chunk based on time or buffer fullness
	if c.bufferPos >= c.chunkSamples {
		c.createChunk()
		c.slideBuffer()
	}
}

func (c *RingChunker) createChunk() {
	if c.bufferPos < c.chunkSamples {
		return
	}

	// Create chunk data
	chunkPCM := make([]int16, c.chunkSamples)
	copy(chunkPCM, c.buffer[:c.chunkSamples])

	// Determine start and end times
	startTime := c.timestamps[0]
	endTime := c.timestamps[c.chunkSamples-1]

	// Collect unique speakers in this chunk
	speakerMap := make(map[string]struct{})
	for i := 0; i < c.chunkSamples; i++ {
		for _, speaker := range c.speakers[i] {
			if speaker != "" {
				speakerMap[speaker] = struct{}{}
			}
		}
	}

	speakerList := make([]string, 0, len(speakerMap))
	for speaker := range speakerMap {
		speakerList = append(speakerList, speaker)
	}

	chunk := &Chunk{
		ID:       uuid.New(),
		PCM:      chunkPCM,
		Start:    startTime,
		End:      endTime,
		Speakers: speakerList,
	}

	select {
	case c.chunkChan <- chunk:
		log.Debug().
			Str("chunk_id", chunk.ID.String()).
			Time("start", chunk.Start).
			Time("end", chunk.End).
			Strs("speakers", chunk.Speakers).
			Int("samples", len(chunk.PCM)).
			Msg("Created audio chunk")
	case <-c.stopChan:
		return
	default:
		log.Warn().Msg("Chunk channel full, dropping chunk")
	}

	c.lastChunkTime = endTime
}

func (c *RingChunker) slideBuffer() {
	// Slide buffer by (chunkSamples - overlapSamples) to maintain overlap
	slideAmount := c.chunkSamples - c.overlapSamples
	
	if slideAmount > 0 && slideAmount < len(c.buffer) {
		// Move remaining samples to beginning
		copy(c.buffer, c.buffer[slideAmount:c.bufferPos])
		copy(c.timestamps, c.timestamps[slideAmount:c.bufferPos])
		copy(c.speakers, c.speakers[slideAmount:c.bufferPos])
		
		c.bufferPos -= slideAmount
	} else {
		// Start fresh if slide would remove everything
		c.bufferPos = 0
	}
}

func (c *RingChunker) GetChunk() <-chan *Chunk {
	return c.chunkChan
}

func (c *RingChunker) Stop() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.stopped {
		return
	}

	c.stopped = true
	close(c.stopChan)

	// Create final chunk if there's remaining data
	if c.bufferPos > c.overlapSamples {
		finalPCM := make([]int16, c.bufferPos)
		copy(finalPCM, c.buffer[:c.bufferPos])

		startTime := c.timestamps[0]
		endTime := c.timestamps[c.bufferPos-1]

		speakerMap := make(map[string]struct{})
		for i := 0; i < c.bufferPos; i++ {
			for _, speaker := range c.speakers[i] {
				if speaker != "" {
					speakerMap[speaker] = struct{}{}
				}
			}
		}

		speakerList := make([]string, 0, len(speakerMap))
		for speaker := range speakerMap {
			speakerList = append(speakerList, speaker)
		}

		finalChunk := &Chunk{
			ID:       uuid.New(),
			PCM:      finalPCM,
			Start:    startTime,
			End:      endTime,
			Speakers: speakerList,
		}

		select {
		case c.chunkChan <- finalChunk:
			log.Debug().
				Str("chunk_id", finalChunk.ID.String()).
				Msg("Created final audio chunk")
		default:
			log.Warn().Msg("Could not send final chunk")
		}
	}

	close(c.chunkChan)
}
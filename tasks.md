# tasks.md

## 0. Repo scaffolding
- [ ] `go mod init github.com/<you>/discord-notetaker`
- [ ] Top‑level directories:
  - `cmd/discord-notetaker/main.go`
  - `internal/bot`, `internal/audio`, `internal/stt/{vosk,deepgram}`, `internal/summariser/gemini`, `internal/config`, `internal/store`.
  - `assets/sounds/start.ogg`
  - `models/vosk/…` (optional — instruct user to download instead).

## 1. Config & logging
- [ ] Implement `config.Load()` from `.env` + env with defaults.
- [ ] Implement `logger` (zerolog or slog) with leveled logs.

## 2. Discord voice plumbing
- [ ] Connect to gateway with intents for voice + messages.
- [ ] Implement `!join`/`!leave` handlers.
- [ ] Voice join, play chime, start `OpusRecv` loop.

## 3. Audio pipeline
- [ ] `audio.Decoder` — Opus → PCM (`gopus`).
- [ ] `audio.VAD` — wrapper for WebRTC VAD; fallback to RMS threshold.
- [ ] `audio.Chunker` — rolling window, overlap, emits `Chunk{ID, PCM, Start, End, Speakers}`.
- [ ] Unit tests: chunk sizes, overlap, silence boundaries.

## 4. STT interfaces
- [ ] Define `type Transcriber interface { Transcribe(ctx context.Context, c *audio.Chunk) ([]audio.Utterance, error) }`.
- [ ] Implement `VoskTranscriber`.
- [ ] Implement `DeepgramTranscriber` (streaming and/or prerecorded paths).
- [ ] Worker pool with back‑pressure; metrics: queue depth, chunk latency.

## 5. Transcript assembly
- [ ] `store.Writer` to append JSONL utterances and roll files per session.
- [ ] `assembler` to merge short utterances into sentences based on punctuation & gaps.

## 6. Summariser
- [ ] Gemini client wrapper; choose backend (Gemini API vs Vertex) from config.
- [ ] Implement `Summarise(ctx, transcript []Utterance) (markdown string, error)`.
- [ ] Handle long transcripts: hierarchical (map‑reduce) summarisation.

## 7. Posting results
- [ ] Upload files to channel; post compact preview message with key bullets and action items.
- [ ] Ephemeral progress updates to the command user (optional).

## 8. QA & benchmarks
- [ ] Measure end‑to‑end latency (speech → caption, speech → notes).
- [ ] CPU/RAM profiles for Vosk on your hardware.
- [ ] Load test with synthetic audio.

## 9. Ops & security
- [ ] Secrets via env only; no plaintext logs.
- [ ] Graceful shutdown on SIGTERM; drain queues.
- [ ] Retry with jitter; idempotent uploads.

## 10. Nice‑to‑haves
- [ ] Live captions to a text channel thread.
- [ ] Word‑timestamped subtitle (WebVTT) export.
- [ ] On‑device Whisper.cpp backend as a third option.

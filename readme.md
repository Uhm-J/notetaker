# README.md

## Discord Notetaker Bot (Go)

A Discord bot that joins a voice channel on command, plays a short start tone, records audio, transcribes it to text, and produces structured notes. Two STT backends are supported:

- **Local**: Vosk (offline). Chunked streaming to avoid long waits.
- **Cloud**: Deepgram (streaming / prerecorded). Lower latency, higher accuracy, pay‑per‑use.

Summaries/notes are generated with **Gemini Flash** via Google’s Gen AI Go SDK.

---

## Feature set

- `!join` — join the caller’s voice channel, play a chime, begin capture & live partial captions (optional).
- `!stop` — stop capture; flush outstanding chunks; post transcript + notes.
- `!mode` — set summary style (`brief`, `verbose`, `casual`, `formal`).
- `!retry` — regenerate notes with an optional mode.
- Chunked processing (default **5 s** windows, overlap 300 ms) to amortize latency and enable incremental notes.
- Diarization: per‑user tagging from Discord voice state + (optionally) STT diarization to resolve overlaps.
- Resilience: back‑pressure on the STT worker pool, automatic reconnect, graceful shutdown.
- Storage: JSONL transcript with timestamps + Markdown notes artifact.

---

## High‑level architecture

```
Discord VC  ──opus frames──▶ Audio Capture
                               │
                               ▼
                        Opus → PCM (48 kHz mono)
                               │
                      ring buffer / chunker
                  ┌──────┬───────────────┬──────┐
                  ▼      ▼               ▼      ▼
                STT workers (N)
           (Vosk local or Deepgram API)
                  │          
                  └──► normalized transcript stream
                                     │
                                     ▼
                              Notes pipeline
                         (Gemini Flash summariser)
                                     │
                                     ▼
                        Markdown notes + JSONL transcript
```

---

## Go packages

- **Discord & audio**
  - `github.com/bwmarrin/discordgo` — Discord gateway + voice.
  - `layeh.com/gopus` — Opus decode to PCM.
  - `github.com/bwmarrin/dgvoice` — small helpers (optional; for play/record patterns).
- **STT backends**
  - **Local**: `github.com/alphacep/vosk-api/go`.
  - **Cloud**: `github.com/deepgram/deepgram-go-sdk/v2/deepgram`.
- **VAD (optional but recommended)**
  - `github.com/aflyinghusky/go-webrtcvad` (or `github.com/maxhawkins/go-webrtcvad`).
- **Summarisation**
  - `google.golang.org/genai` — Google Gen AI SDK (Gemini Flash).
- **Config & utils**
  - `github.com/joho/godotenv` — load `.env`.
  - `golang.org/x/sync/errgroup` — coordinated worker shutdown.
  - `github.com/google/uuid` — chunk IDs.
  - `github.com/rs/zerolog` — structured logging (or `log/slog`).

---

## Configuration

Create a `.env` file:

```
DISCORD_TOKEN=xxxx
# Choose one STT path
STT_BACKEND=vosk            # or deepgram
VOSK_MODEL_PATH=./models/vosk/en

DEEPGRAM_API_KEY=dg_xxx
DEEPGRAM_TIER=nova-2        # default model
DEEPGRAM_DIARIZE=true
DEEPGRAM_PUNCTUATE=true
DEEPGRAM_UTTERANCES=true

# Gemini (Gemini API) — or configure Vertex; both supported by the SDK
GENAI_API_KEY=ya29.xxxx
GENAI_BACKEND=gemini        # gemini | vertex
GENAI_MODEL=gemini-2.5-flash

# Chunking
CHUNK_SECONDS=5
CHUNK_OVERLAP_MS=300
MAX_PARALLEL_STT=4
```

---

## Command flow

- `!join`
  1. Find author’s current voice channel.
  2. Connect voice; start goroutine reading `VoiceConnection.OpusRecv`.
  3. Play chime via Opus writer.
  4. Begin chunker + STT workers.

- `!stop`
  1. Stop capture; close chunker; wait for workers to drain.
  2. Finalize transcript JSONL and call Gemini to produce notes.
  3. Upload transcript (`.jsonl`) and notes (`.md`) to the text channel.
- `!mode`
  1. Change how concise or formal the notes are.
- `!retry`
  1. Reprocess the latest transcript with the current or given mode.

---

## Audio & chunking details

- **Discord audio**: 48 kHz, 20 ms Opus frames. Decode with `gopus` to 16‑bit PCM mono.
- **Silence detection**: treat Opus comfort‑noise frames (`F8 FF FE`) as silence boundaries; additionally gate with WebRTC VAD on PCM to avoid false edges.
- **Chunk window**: default **5 seconds** (240,000 samples). Maintain a short **overlap** (~300 ms) to preserve context for STT.
- **Diarization**: tag samples with current speaking user when available; if multiple users overlap, prefer STT diarization labels as secondary evidence.
- **Back‑pressure**: a bounded `chan *Chunk` feeds a worker pool (size `MAX_PARALLEL_STT`). If the queue fills, drop overlap first, then expand silence coalescing.

---

## STT backends

### Local — Vosk

- Initialize once per process: `vosk.NewModel(path)`.
- One recognizer per worker: `vosk.NewRecognizer(model, 48000)`.
- Feed each chunk with `AcceptWaveform(pcm)`; read `FinalResult()` JSON (contains text + timing).
- Pros: private, offline, predictable cost.
- Cons: slower on CPU; models are large; accuracy below SOTA.

### Cloud — Deepgram

- Use **streaming** for low‑latency captions, or **prerecorded** for post‑meeting accuracy.
- Recommended options: `model=nova-2`, `punctuate=true`, `diarize=true`, `utterances=true`, `smart_format=true`, `detect_language=true` when uncertain.
- Pros: strong accuracy, diarization, word timings; scales easily.
- Cons: paid; requires network; handle retries & rate limits.

---

## Summarisation — Gemini Flash

- Model: `gemini-2.5-flash` (fast) for first pass; optionally rerank with a slower model if you want premium quality.
- Prompt template:

```
You are a meeting notetaker. Given a diarized transcript with timestamps, produce:
1) bullet point summary (max 12 bullets);
2) decisions; 3) action items {assignee, task, due_if_stated};
4) open questions; 5) key references with timestamps.
Return Markdown.
```

- Provide the (possibly long) transcript as concatenated JSONL excerpts; if it exceeds token limits, chunk the transcript and ask Gemini to produce partial section notes, then merge with a final synthesis prompt.

---

## Output artifacts

- `transcripts/<session-id>.jsonl` — one JSON object per utterance `{ts_start, ts_end, user_id, user_tag, text, source: "vosk|deepgram"}`.
- `notes/<session-id>.md` — Markdown notes.

---

## Run locally

```
go mod tidy
GOOS=$(go env GOOS) GOARCH=$(go env GOARCH) go run ./cmd/discord-notetaker
```

Invite bot and use `!join` / `!stop` in a text channel. Use `!help` for all commands.

---

## Production hardening checklist

- Use systemd or a container with a health‑check.
- Persist artifacts to S3‑compatible storage.
- Structured logging + retention; redact secrets.
- Exponential backoff for Deepgram / Gemini calls.
- Rate‑limit commands per guild.
- Automatic model warm‑up on boot; readiness probe waits for first successful STT.

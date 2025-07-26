# Discord Notetaker Bot Setup Guide

## Prerequisites

- Go 1.21 or higher
- Discord Bot Token
- API keys for chosen services (Gemini, Deepgram)

## Quick Start

1. **Clone and setup project**:
   ```bash
   git clone <repo-url>
   cd discord-notetaker
   make setup
   ```

2. **Configure environment**:
   ```bash
   cp .env.example .env
   # Edit .env with your API keys
   ```

3. **Choose your STT backend**:

   ### Option A: Vosk (Local, Offline)
   ```bash
   # Download a Vosk model
   cd models/vosk
   wget https://alphacephei.com/vosk/models/vosk-model-en-us-0.22.zip
   unzip vosk-model-en-us-0.22.zip
   mv vosk-model-en-us-0.22 en
   ```

   ### Option B: Deepgram (Cloud, Higher Quality)
   - Just set your Deepgram API key in .env
   - No model download needed

4. **Get API keys**:

   ### Discord Bot Token
   - Go to https://discord.com/developers/applications
   - Create a new application
   - Go to "Bot" section
   - Copy the token

   ### Gemini API Key
   - Go to https://makersuite.google.com/app/apikey
   - Create an API key
   - Copy the key

   ### Deepgram API Key (if using Deepgram)
   - Go to https://console.deepgram.com/
   - Create an account and project
   - Copy your API key

5. **Configure your .env file**:
   ```env
   DISCORD_TOKEN=your_discord_bot_token
   STT_BACKEND=vosk  # or deepgram
   GENAI_API_KEY=your_gemini_api_key
   # ... other settings
   ```

6. **Invite bot to Discord**:
   - In Discord Developer Portal, go to OAuth2 > URL Generator
   - Select scopes: `bot`
   - Select permissions: `View Channels`, `Send Messages`, `Connect`, `Speak`, `Use Voice Activity`
   - Copy and visit the generated URL

7. **Run the bot**:
   ```bash
   make run
   ```

## Usage

### Commands

- `!join` - Join your current voice channel and start recording
- `!leave` - Stop recording and generate notes

### Example Flow

1. Join a voice channel in Discord
2. Type `!join` in a text channel
3. The bot joins and starts recording
4. Have your meeting/conversation
5. Type `!leave` when done
6. Bot processes the audio and posts transcript + notes

## Configuration Options

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DISCORD_TOKEN` | Discord bot token | Required |
| `STT_BACKEND` | Speech-to-text backend (`vosk` or `deepgram`) | `vosk` |
| `GENAI_API_KEY` | Gemini API key | Required |
| `CHUNK_SECONDS` | Audio chunk size in seconds | `5` |
| `MAX_PARALLEL_STT` | Number of parallel STT workers | `4` |
| `LOG_LEVEL` | Logging level (`debug`, `info`, `warn`, `error`) | `info` |

### STT Backend Comparison

| Feature | Vosk | Deepgram |
|---------|------|----------|
| **Cost** | Free | Pay-per-use |
| **Quality** | Good | Excellent |
| **Latency** | Higher | Lower |
| **Privacy** | Complete (offline) | Cloud-based |
| **Diarization** | No | Yes |
| **Setup** | Model download required | API key only |

## Troubleshooting

### Common Issues

1. **Bot doesn't respond to commands**:
   - Check bot permissions in Discord
   - Verify bot token is correct
   - Check bot is online

2. **Audio not recording**:
   - Ensure bot has voice permissions
   - Check if someone is speaking in voice channel
   - Verify audio pipeline logs

3. **Transcription errors**:
   - For Vosk: Check model path is correct
   - For Deepgram: Verify API key and quota
   - Check network connectivity

4. **Summarization fails**:
   - Verify Gemini API key
   - Check API quota/limits
   - Ensure transcript has content

### Logs

Enable debug logging to troubleshoot:
```env
LOG_LEVEL=debug
```

### Performance Tuning

- **CPU intensive (Vosk)**: Reduce `MAX_PARALLEL_STT`
- **Memory usage**: Reduce `CHUNK_SECONDS`
- **Network usage (Deepgram)**: Increase `CHUNK_SECONDS`

## Production Deployment

### Systemd Service

```ini
[Unit]
Description=Discord Notetaker Bot
After=network.target

[Service]
Type=simple
User=discord-bot
WorkingDirectory=/opt/discord-notetaker
ExecStart=/usr/local/bin/discord-notetaker
Restart=always
RestartSec=10
Environment=ENVIRONMENT=production

[Install]
WantedBy=multi-user.target
```

### Docker

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o discord-notetaker ./cmd/discord-notetaker

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/discord-notetaker .
CMD ["./discord-notetaker"]
```

## Security Notes

- Keep API keys secure and never commit them to version control
- Use environment variables or secret management in production
- Consider running in a container for isolation
- Monitor API usage and costs
- Regularly rotate API keys
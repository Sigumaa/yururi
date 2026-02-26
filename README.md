# yururi

Discord向け自律エージェント「ゆるり」。

## 構成

- Codex App Server (`codex --search app-server --listen stdio://`)
- Discord inbound handler
- MCP server (`/mcp`) with Discord tools + memory tools + utility tools
- Markdown memory store (`memory/`)
- Heartbeat cron runner

## 必要環境

- Go 1.23+
- `codex` コマンド
- Discord Bot Token

## 設定

`runtime/config.example.yaml` を `runtime/config.yaml` にコピーして設定する。

主な設定キー:

- `discord.guild_id`
- `discord.target_channel_ids[]`
- `persona.owner_user_id`
- `codex.command`
- `codex.args`
- `codex.workspace_dir`
- `codex.home_dir`
- `mcp.bind`
- `mcp.url`
- `heartbeat.enabled`
- `heartbeat.cron`
- `heartbeat.timezone`
- `memory.root_dir`

## 起動

```bash
export CODEX_HOME="$PWD/runtime/.codex-home"
go run ./cmd/yururi -config runtime/config.yaml
```

起動時に `workspace_dir` 配下へ次のファイルを自動生成する。

- `YURURI.md`
- `SOUL.md`
- `MEMORY.md`
- `HEARTBEAT.md`

## 実装済みMCP tools

- `read_message_history`
- `send_message`
- `reply_message`
- `add_reaction`
- `start_typing`
- `list_channels`
- `get_user_detail`
- `get_current_time`
- `memory_upsert_user_note`
- `memory_upsert_channel_intent`
- `memory_upsert_task`
- `memory_query`

## 検証

```bash
go fmt ./...
go test ./...
go vet ./...
```

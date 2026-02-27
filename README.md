# yururi

Discord向け自律エージェント「ゆるり」。

## 構成

- Codex App Server (`codex --search app-server --listen stdio://`)
- Discord inbound handler
- MCP server (`/mcp`) with Discord tools + workspace doc tools + utility tools
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
- `discord.observe_channel_ids[]`
- `discord.observe_category_ids[]`
- `persona.owner_user_id`
- `persona.times_channel_id`
- `persona.times_min_interval_sec`
- `codex.command`
- `codex.args`
- `codex.workspace_dir`
- `codex.home_dir`
- `codex.mcp_servers.*`
- `mcp.bind`
- `mcp.url`
- `mcp.tool_policy.allow_patterns[]`
- `mcp.tool_policy.deny_patterns[]`
- `heartbeat.enabled`
- `heartbeat.cron`
- `heartbeat.timezone`
- `autonomy.enabled`
- `autonomy.cron`
- `autonomy.timezone`
- `xai.enabled`
- `xai.api_key`
- `xai.base_url`
- `xai.model`
- `xai.timeout_sec`

`mcp.tool_policy.*` は `*` ワイルドカード対応、大小文字を区別しない。
`x_search` を使う場合は `xai.enabled=true` と `xai.api_key` を設定する。
`twilog-mcp` を使う場合は `codex.mcp_servers.twilog-mcp.bearer_token` を設定できる。`mcp-remote` 利用時は `--header Authorization: Bearer ...` も自動で付与する。`CODEX_MCP_TWILOG_BEARER_TOKEN` も引き続き使え、設定時は環境変数を優先する。
`discord.observe_category_ids[]` を設定した場合は、カテゴリ配下のテキストチャンネルを起動時に観察対象へ追加する。
ログ色付けはTTY接続時に自動有効。`NO_COLOR` で無効化、`YURURI_LOG_COLOR=true/false` で強制できる。

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
- `x_search`
- `read_workspace_doc`
- `append_workspace_doc`
- `replace_workspace_doc`

`send_message` と `reply_message` は既定でURLプレビューを抑制する。

## 検証

```bash
go fmt ./...
go test ./...
go vet ./...
```

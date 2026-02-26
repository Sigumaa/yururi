# yururi

Discord向けエージェント「ゆるり」のPhase 1最小実装。

## 必要環境

- Go 1.22+
- `codex`コマンド（`--search app-server --listen stdio://`で起動可能）
- Discord Bot Token

## 設定

`runtime/config.example.yaml` を `runtime/config.yaml` にコピーして作成する。

```yaml
discord:
  token: "YOUR_DISCORD_BOT_TOKEN"
  guild_id: "YOUR_GUILD_ID"
  target_channel_ids:
    - "TARGET_CHANNEL_ID"
  excluded_channel_ids: []
  allowed_bot_user_ids: []
persona:
  owner_user_id: "OWNER_USER_ID"
codex:
  command: "codex"
  args: ["--search", "app-server", "--listen", "stdio://"]
  workspace_dir: "./runtime/workspace"
  home_dir: "./runtime/.codex-home"
```

`cwd/home` の代わりに `workspace_dir/home_dir` も利用可能。

環境変数で上書き可能:

- `DISCORD_TOKEN`
- `DISCORD_GUILD_ID`
- `DISCORD_TARGET_CHANNEL_IDS`（カンマ区切り）
- `DISCORD_EXCLUDED_CHANNEL_IDS`（カンマ区切り）
- `DISCORD_ALLOWED_BOT_USER_IDS`（カンマ区切り）
- `PERSONA_OWNER_USER_ID`
- `CODEX_COMMAND`
- `CODEX_ARGS`（JSON配列またはカンマ区切り）
- `CODEX_CWD`
- `CODEX_WORKSPACE_DIR`
- `CODEX_HOME`
- `CODEX_HOME_DIR`

## 起動

```bash
export CODEX_HOME="$PWD/runtime/.codex-home"
go run ./cmd/yururi -config runtime/config.yaml
```

## 検証

```bash
go fmt ./...
go test ./...
go vet ./...
```

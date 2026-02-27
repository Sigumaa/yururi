# ゆるり実装計画 v4（着手用・決定版）

## Summary
1. Goで単一Discordサーバー専用の高自律エージェント「ゆるり」を実装する。
2. 指定チャンネル投稿はメンション不要で処理候補にし、返信可否はAI判断に委ねる。
3. Codex実行は`codex --search app-server --listen stdio://`を既定にし、Web検索を常時有効化する。
4. Discord操作は内蔵MCP tool、Discord以外の独自util第一弾は`get_current_time(timezone?)`を追加する。
5. 会話本文は永続化しない。AI自己判断で4軸Markdown（YURURI/SOUL/MEMORY/HEARTBEAT）を更新する。
6. 定期実行はheartbeat駆動で行い、必要時のみ投稿する。

## 目的と成功基準
1. 指定チャンネルのみで自然参加し、不要返信を増やさない。
2. 「これはこういう意味」の学習を再起動後も反映できる。
3. 開発者本人の「毎日〜して」を自動タスク化し、heartbeatで継続実行できる。
4. 調査投稿の形式はAI裁量とし、必要に応じてWeb検索を使える。

## 公開インターフェース/型（実装で固定）
1. `config.yaml`公開キー:
   - `discord.guild_id`
   - `discord.target_channel_ids[]`
   - `discord.excluded_channel_ids[]`
   - `discord.allowed_bot_user_ids[]`
   - `persona.owner_user_id`
   - `codex.command`（既定: `codex`）
   - `codex.args`（既定: `["--search","app-server","--listen","stdio://"]`）
   - `codex.workspace_dir`
   - `codex.home_dir`
   - `heartbeat.cron`（既定30分）
   - `heartbeat.timezone`
   - `xai.enabled`
   - `xai.api_key`
   - `xai.base_url`（既定: `https://api.x.ai/v1`）
   - `xai.model`（既定: `grok-4-1-fast-non-reasoning`）
   - `xai.timeout_sec`
2. 主要型:
   - `IncomingEvent`
   - `DecisionResult`（`noop|reply|react|send`）
   - `ChannelSessionState`
   - `MCPToolCall`
3. 主要IF:
   - `AiRuntime.RunTurn(ctx, input) (output, err)`
   - `DiscordGateway`（send/reply/react/typing/history）
   - `SessionCoordinator`（channelごとのthread/turn継続管理）

## アーキテクチャ
1. `cmd/yururi/main.go`で設定読込・依存配線・起動。
2. `internal/discord/inbound`で`messageCreate`受信。
3. `internal/policy`でサーバー/チャンネル/Bot可否を判定。
4. `internal/context`で現在投稿+直近メモリ+Markdown知識を入力化。
5. `internal/ai/codex`でapp-server JSON-RPC制御。
6. `internal/mcp/server`でtoolを登録しCodexから呼ばせる。
7. `internal/heartbeat`で定期実行しタスク処理。
8. `workspace`配下の4軸Markdownをtool経由で更新する。

## Tool設計（MCP）
1. Discord tools:
   - `read_message_history(channel_id, before_message_id?, limit<=100)`
   - `send_message(channel_id, content)`
   - `reply_message(channel_id, reply_to_message_id, content)`
   - `add_reaction(channel_id, message_id, emoji)`
   - `start_typing(channel_id, source, duration_sec?)`
2. Utility tools:
   - `get_current_time(timezone?)`（未指定時`Asia/Tokyo`）
   - `x_search(query, allowed_x_handles?, excluded_x_handles?, from_date?, to_date?, enable_image_understanding?, enable_video_understanding?)`
3. Workspace doc tools:
   - `read_workspace_doc`
   - `append_workspace_doc`
   - `replace_workspace_doc`
4. `read_message_history`は複数回呼び出し前提で実装する。

## 永続メモリ（Markdown）
1. `workspace/YURURI.md`
2. `workspace/SOUL.md`
3. `workspace/MEMORY.md`
4. `workspace/HEARTBEAT.md`
5. 保存対象は抽出知識のみ。会話本文は保存しない。
6. 更新はAI自己判断、矛盾時は新情報で上書きする。

## 指示ファイル読込
1. 起動時に存在するものだけ読む。
2. 対象: `YURURI.md` `SOUL.md` `MEMORY.md` `HEARTBEAT.md`
3. 読込順: `YURURI -> SOUL -> MEMORY -> HEARTBEAT`
4. 欠損時は警告のみで継続。

## 実行フロー
1. 投稿イベント受信。
2. フィルタ判定（guild/channel/bot）。
3. コンテキスト構築。
4. Codex turn開始。
5. 必要に応じてMCP tool呼び出し。
6. 最終`DecisionResult`反映。
7. typing停止・メタログ出力。
8. heartbeat時は`HEARTBEAT.md`の指示に従って必要時のみ行動する。

## 制約と運用ルール
1. 指定チャンネル外では動作しない。
2. Bot/Webhookは許可IDのみ処理。
3. owner優遇は口調のみ、権限拡張はしない。
4. 返信頻度のハード上限は設けない（AI裁量）。
5. Web調査投稿のリンク有無はAI裁量。

## テストケース
1. フィルタ判定（guild/channel/exclude/allowed_bot）。
2. ownerトーン分岐。
3. `get_current_time`のtimezone解決。
4. 4軸Markdown更新（read/append/replace）の正常系。
5. heartbeat実行時に不要投稿しないこと。
6. `read_message_history`複数回呼び出し。
7. `noop`時にDiscord投稿しないこと。
8. reply/react/send/typingの正常系。
9. 会話本文が永続化されないこと。
10. `codex --search app-server`起動失敗時のフォールト処理。
11. `x_search` 呼び出し時の正常系・エラー系。

## 実装フェーズ
1. Phase 1: 設定・Discord受信・Codex最小連携・`noop/reply`。
2. Phase 2: Discord MCP tools一式。
3. Phase 3: 4軸Markdown更新導線。
4. Phase 4: heartbeat駆動実行。
5. Phase 5: utility tool（時刻）追加とテスト拡充。

## 明示前提
1. 実行環境はローカルMacBook。
2. `codex`バイナリはPATH固定（切替機能は入れない）。
3. Web検索は常時有効で起動する。
4. 定期依頼自動登録は`owner_user_id`のみ。
5. 本計画は実装着手時にそのままタスク分解可能な粒度で確定済み。

## 追補（2026-02-27）
1. リアーキテクトの実装順序と詳細要件は`docs/REARCHITECTURE.md`を正とする。
2. `thread/start`毎回作成の挙動は段階的に廃止し、チャンネルセッション＋`turn/steer`中心へ移行する。
3. 4軸Markdown（`YURURI.md`/`SOUL.md`/`MEMORY.md`/`HEARTBEAT.md`）を主記憶として扱う。
4. 本書と`docs/REARCHITECTURE.md`で差異が出た場合は、`docs/REARCHITECTURE.md`を優先する。

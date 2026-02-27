# ゆるり リアーキテクト方針 v1

## 目的
1. 会話継続性を上げる。
2. 自律的なtool useを壊さずに安全性と可観測性を上げる。
3. 応答体験を改善し、無駄な待ち時間と不要な`noop`を減らす。

## 現状の主要課題
1. 毎ターン`thread/start`しており、チャンネル会話の文脈が途切れやすい。
2. `turn/steer`が未導入で、連続投稿時の追記制御が弱い。
3. セッション制御は`dispatcher`のみで、AIターン状態を持つ調停層がない。
4. Memoryが`runtime/memory`と4軸Markdownで分かれており、責務が曖昧。
5. ツール実行ポリシーと観測項目が最小で、運用時の原因追跡が難しい。

## ターゲットアーキテクチャ
1. `internal/orchestrator`を新設し、`channel_key(guild:channel)`単位でセッションを管理する。
2. セッション状態として`thread_id`、`active_turn_id`、`phase`、`last_activity`を保持する。
3. 初回は`thread/start -> turn/start`、継続は`turn/steer`優先、失敗時は`turn/start`へフォールバックする。
4. Discordイベント処理は「受信→フィルタ→セッションキュー→コンテキスト構築→Codex実行→tool結果反映→ログ記録」に固定する。
5. 4軸Markdown(`YURURI.md/SOUL.md/MEMORY.md/HEARTBEAT.md`)を主記憶とし、`runtime/memory/tasks`はスケジュール実行用途に限定する。
6. MCP toolは`allow/deny`プロファイルで制御し、危険操作は既定拒否とする。
7. 構造化ログに`session_key`、`run_id`、`thread_id`、`turn_id`、`queue_wait_ms`、`turn_latency_ms`、`tool_calls`を必ず出す。

## 実装フェーズ（再編）
1. R1 Session Coordinator導入
   - `channel -> session`管理と同一チャネル直列処理を`orchestrator`へ移す。
   - 完了条件: 同一チャネル同時投稿で順序が崩れない。
2. R2 Codex Turn制御拡張
   - `startThread/startTurn/steerTurn`APIを`internal/codex`へ追加する。
   - 完了条件: 継続投稿で`turn/steer`が使われ、失敗時にフォールバックする。
3. R3 Prompt/Memory責務整理
   - 4軸Markdown優先の読み書き導線を固定し、会話本文非永続化を維持する。
   - 完了条件: 「覚えて」要求が4軸Markdownへ反映される。
4. R4 Tool Governance
   - MCP toolの許可セットと拒否理由を明示ログ化する。
   - 完了条件: 禁止tool呼び出しが拒否され、理由がログに残る。
5. R5 Observability
   - 実行IDと各種レイテンシ計測を追加し、ボトルネックを追えるようにする。
   - 完了条件: 1ターンの待機時間と実行時間をログで追跡できる。
6. R6 自律運用仕上げ
   - heartbeatとタスク実行をセッション制御と統合し、重複実行を防ぐ。
   - 完了条件: heartbeat競合時も同一タスクが二重実行されない。

## 参照方針
1. `luna-chat`はセッション調停と`turn/steer`運用を参考にする。
2. `openclaw`はキュー階層、ツール統治、観測設計を参考にする。
3. 詳細仕様の一次ソースは本リポジトリの`docs/SPEC.md`と本書とする。

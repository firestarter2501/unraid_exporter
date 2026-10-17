# unraid_exporter

[Unraid API](https://docs.unraid.net/API/)（GraphQL）からデータを取得する Prometheus exporter です。

- Unraid 側にプラグインやスクリプトを追加する必要はなく、公式 API を **読み取り専用の API キー**（`VIEWER` ロール）で叩くだけです
- エリアごとに GraphQL クエリを分けて並列実行します。古い Unraid API にあるフィールドが無くても、失敗するのはその collector だけで、`unraid_exporter_collector_success` で確認できます
- 一部のフィールドだけが失敗した部分エラー（例: アレイ停止中のディスク温度 `NaN`）では、取得できた値は出力し、その系列だけを省きます

## 構成

- **本番環境：** Unraid の Docker で動かします。イメージは GitHub Actions がビルドし、`ghcr.io/firestarter2501/unraid_exporter` に公開します
- **開発環境：** podman だけを使います。ビルド・テスト・lint・E2E はすべてコンテナ内で行い、ホストには何もインストールしません（Go のキャッシュは podman の名前付きボリュームに置きます）
- **対応する Unraid：** API が有効な Unraid 7.x 以降（またはプラグイン版 Unraid Connect / API）

## Unraid API キーの作成

WebGUI の **Settings → Management Access → API Keys** で、`VIEWER` ロールのキーを作成します。Unraid のシェルからなら次のとおりです。

```sh
unraid-api apikey --create --name prometheus --roles VIEWER --description "Prometheus exporter"
```

## Unraid へのデプロイ

### Docker テンプレート（推奨）

1. Unraid のシェルでテンプレートを取得します

   ```sh
   wget -O /boot/config/plugins/dockerMan/templates-user/my-unraid-exporter.xml \
     https://raw.githubusercontent.com/firestarter2501/unraid_exporter/main/deploy/unraid/unraid-exporter.xml
   ```

2. **Docker → Add Container → Template** で `unraid-exporter` を選びます
3. **Unraid URL** と **API key** を入力して **Apply** を押します
   - Unraid URL には、コンテナ（bridge ネットワーク）から届く Unraid 自身の IP アドレスを指定します（例: `http://192.168.1.10`）。`localhost` はコンテナ自身を指すため使えません
   - API key の欄は画面上でマスクされます
4. 動作を確認します：`curl -s http://<unraid>:9703/metrics | grep '^unraid_up'`

テンプレートの **WebUI** から `/metrics` を開けます。イメージに `HEALTHCHECK` が入っているので、Docker 画面にヘルス状態も表示されます。イメージの更新は、Unraid の通常の更新チェックで行えます。

ネットワークについて：br0 などの macvlan / ipvlan のカスタムネットワークでは、コンテナからホスト自身に届かないため、デフォルトの `bridge` のまま使ってください。どうしても使う場合は、Docker 設定の「Host access to custom networks」を有効にしてください。

### Docker Compose Manager プラグイン / docker compose

```sh
cp deploy/unraid-exporter.env.example deploy/unraid-exporter.env   # 編集する
docker compose -f deploy/compose.yaml up -d
```

### Prometheus の設定

[`deploy/prometheus.yml`](deploy/prometheus.yml) と、アラートルールの例 [`deploy/alerts.yml`](deploy/alerts.yml) を参照してください。

## 設定

**設定はすべて環境変数で指定できます。** 同じ設定はコマンドラインフラグでも指定でき、両方あればフラグが優先されます。不正な値（例: `UNRAID_TIMEOUT=10`）を指定すると、黙ってデフォルトに戻すのではなく、エラーメッセージを出して起動を中止します。

| 環境変数 | フラグ | デフォルト | |
|---|---|---|---|
| `UNRAID_URL` | `--unraid.url` | （必須） | Unraid の URL。`/graphql` は自動で付きます |
| `UNRAID_API_KEY` | `--unraid.api-key` | （必須*） | API キー |
| `UNRAID_API_KEY_FILE` | `--unraid.api-key-file` | | API キーを書いたファイル。`UNRAID_API_KEY` より優先されます。コンテナの uid 65532 から読める権限が必要です |
| `UNRAID_INSECURE_SKIP_VERIFY` | `--unraid.insecure-skip-verify` | `false` | https で自己署名証明書を許可する |
| `UNRAID_TIMEOUT` | `--unraid.timeout` | `10s` | スクレイプ 1 回あたりのタイムアウト（Go の duration 形式） |
| `UNRAID_EXPORTER_COLLECTORS` | `--collectors` | `disks` 以外すべて（Unraid テンプレートでは `disks` を含むすべて） | 有効にする collector（カンマ区切り） |
| `UNRAID_EXPORTER_NETWORK_INCLUDE` | `--collector.network.include` | （空：すべて） | 残すインターフェース名の正規表現（例：`^br0$`）。除外より先に適用されます |
| `UNRAID_EXPORTER_NETWORK_EXCLUDE` | `--collector.network.exclude` | `^(lo\|veth.*)$` | 無視するインターフェース名の正規表現（`^$` にするとすべて出力）。Docker の veth は作り直すたびに名前が変わるため、デフォルトで除外します |
| `UNRAID_EXPORTER_LISTEN_ADDRESS` | `--web.listen-address` | `:9703` | |
| `UNRAID_EXPORTER_TELEMETRY_PATH` | `--web.telemetry-path` | `/metrics` | |
| `UNRAID_EXPORTER_LOG_LEVEL` | `--log.level` | `info` | `debug` / `info` / `warn` / `error` |

\* `UNRAID_API_KEY` と `UNRAID_API_KEY_FILE` のどちらか一方が必須です。

空の環境変数は未設定として扱い、デフォルト値を使います（Unraid は空欄の項目も空文字の環境変数として渡すため）。

Prometheus の `scrape_timeout` は `UNRAID_TIMEOUT` より長くしてください。

## Collectors とメトリクス

単位は Prometheus の慣習に合わせて変換しています（KiB → バイト、% → 0〜1 の比率、MB/s → バイト/秒、GHz → Hz、°F/K → °C）。

| collector | デフォルト | 主なメトリクス |
|---|---|---|
| `info` | ✓ | `unraid_info{hostname,unraid_version,api_version,kernel,arch}`, `unraid_hardware_info`, `unraid_boot_time_seconds`, `unraid_time_seconds`, `unraid_cpu_cores`, `unraid_cpu_threads`, `unraid_cpu_frequency{,_max}_hertz` |
| `array` | ✓ | `unraid_array_state{state}`, `unraid_array_started`, `unraid_array_{size,used,free}_bytes`, `unraid_array_disk_slots{usage}`, `unraid_array_disk_{info（disk_id ラベルに `モデル名_シリアル`）,status,color_info,size_bytes,fs_size_bytes,fs_free_bytes,fs_used_bytes,temperature_celsius,reads_total,writes_total,errors_total,spinning,warning_threshold_ratio,critical_threshold_ratio}`, `unraid_parity_check_{status,running,paused,correcting,progress_ratio,errors,duration_seconds,speed_bytes_per_second,timestamp_seconds}` |
| `metrics` | ✓ | `unraid_cpu_utilization_ratio`, `unraid_cpu_core_utilization_ratio{cpu,mode}`, `unraid_memory_{total,used,free,available,active,buffcache}_bytes`, `unraid_memory_utilization_ratio`, `unraid_swap_{total,used,free}_bytes`, `unraid_swap_utilization_ratio` |
| `temperature` | ✓ ※ | `unraid_temperature_celsius{sensor,name,type,location}`, `unraid_temperature_{warning,critical}_celsius`, `unraid_temperature_status` |
| `network` | ✓ | `unraid_network_up{interface,operstate}`, `unraid_network_{receive,transmit}_{bytes,packets,errors,dropped}_total` |
| `docker` | ✓ | `unraid_docker_containers{state}`, `unraid_docker_container_{info,state,running,autostart,update_available,created_timestamp_seconds}` |
| `vms` | ✓ | `unraid_vm_state{name,state}`, `unraid_vm_running` |
| `shares` | ✓ | `unraid_share_{free,used,size}_bytes{name}` |
| `notifications` | ✓ | `unraid_notifications{folder,importance}` |
| `ups` | ✓ | `unraid_ups_{info,online,battery_charge_ratio,battery_runtime_seconds,input_voltage_volts,output_voltage_volts,load_ratio,nominal_power_watts,power_watts}` |
| `disks` | | `unraid_disk_{info,size_bytes,temperature_celsius,smart_ok,spinning}{device}`：物理ディスクの情報。全ディスクに `smartctl -a` が走るため、exporter のデフォルトでは無効です（Unraid テンプレートでは有効）。モデル名とシリアル番号は `unraid_array_disk_info` の `disk_id` で、ディスクを起こさずに取れます |

exporter 自身のメトリクス：`unraid_up`, `unraid_exporter_collector_success{collector}`, `unraid_exporter_collector_duration_seconds{collector}`, `unraid_exporter_build_info`

状態系のメトリクス（`*_state`, `*_status`）は StateSet 形式で、取りうる状態ごとに系列を出し、現在の状態だけが 1 になります。

### HDD のスピンダウンとの関係

Unraid API（4.37.5）のソースと実機で確認した内容です。

| collector | ディスクへのアクセス | スピンダウン中の HDD |
|---|---|---|
| `array` | emhttp の状態を読むだけ（約 2 ms） | 起こさない |
| `temperature` ※ | 内部で `disks` と同じ処理を呼び、全ディスクに `smartctl -a`（`-n standby` なし）を実行（約 1.2 秒） | **起こしうる** |
| `disks` | 要求したフィールドに関係なく、全ディスクに `smartctl -a` を実行 | **起こしうる** |

SSD は回転しないので、どれを使っても問題ありません。HDD をスピンダウンさせている（Settings → Disk Settings → Default spin down delay が Never 以外の）環境では、`UNRAID_EXPORTER_COLLECTORS` から `temperature` と `disks` を外してください（Unraid テンプレートは `disks` を有効にしています）。ディスク温度は `array` の `unraid_array_disk_temperature_celsius` で、スピンダウン中のディスクを起こさずに取れます（スピンダウン中は出力されません）。外すと CPU やマザーボードの温度は取れなくなります。

実機（Unraid 7.3.3 / API 4.37.5）で確認した API 側の挙動：

- `unraid_array_disk_{reads,writes}_total` は API が 0 を返すことがあります
- `unraid_disk_spinning`（`disks` collector）は、回転中のディスクでも 0 になることがあります。スピン状態を見たいときは `unraid_array_disk_spinning` を使ってください

`VM` 機能や `Docker` を無効にしている場合は、その collector が失敗扱いになります。不要な collector は `UNRAID_EXPORTER_COLLECTORS` から外してください。

## CI / イメージの公開

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) が次を行います。

- **push / PR ごと：** `./dev.sh fmt`、`vet`、`lint`、`test`、`e2e` を、ランナー上の podman で実行します
- **`main` への push：** `ghcr.io/firestarter2501/unraid_exporter:latest` と `:sha-<短縮SHA>` をビルドして公開し、ビルドの provenance を添付します
- **`v1.2.3` 形式のタグの push：** `:1.2.3` と `:1.2` も公開します

## 開発

```sh
./dev.sh help
./dev.sh test     # ユニットテスト（-race）
./dev.sh lint     # golangci-lint
./dev.sh e2e      # モック Unraid + exporter + Prometheus を podman pod で起動して検証
./dev.sh run      # deploy/unraid-exporter.env の設定で、実機に対してローカル実行
./dev.sh check    # fmt + vet + lint + test + e2e
./dev.sh golden   # ゴールデンファイルを再生成
./dev.sh clean    # イメージとキャッシュ用ボリュームを削除
```

テストの内容：

- **ゴールデンテスト**：`internal/collector/testdata/*.json` の GraphQL レスポンスから、collector ごとの出力を `testdata/golden/*.prom` と比較します
- **スキーマ検証**：すべてのクエリを公式スキーマ（`testdata/schema.graphql`。[unraid/api](https://github.com/unraid/api) の `api/generated-schema.graphql`）に対して検証します。スキーマを更新したら、そのファイルを差し替えて `./dev.sh test` を実行してください
- **メトリクス lint**：`promtool` と同じルールで、命名規則や HELP を確認します
- **E2E**：実際のコンテナイメージを、Unraid テンプレートと同じ設定（環境変数のみ、read-only、cap-drop ALL）で起動します。Prometheus にスクレイプさせ、`HEALTHCHECK` が通ることと、不正な環境変数を拒否することも確認します
- **テンプレートの整合性**：`deploy/unraid/unraid-exporter.xml` の変数名とデフォルト値が、exporter のコードと一致しているかを確認します

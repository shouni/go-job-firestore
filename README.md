# 🔥 Go Job Firestore

[![CI](https://github.com/shouni/go-job-firestore/actions/workflows/ci.yml/badge.svg)](https://github.com/shouni/go-job-firestore/actions/workflows/ci.yml)
[![Status](https://img.shields.io/badge/Status-Experimental-orange)](#)
[![Language](https://img.shields.io/badge/Language-Go-blue)](https://go.dev/)
[![Go Version](https://img.shields.io/github/go-mod/go-version/shouni/go-job-firestore)](https://go.dev/)
[![GitHub tag (latest by date)](https://img.shields.io/github/v/tag/shouni/go-job-firestore)](https://github.com/shouni/go-job-firestore/tags)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Reference](https://pkg.go.dev/badge/github.com/shouni/go-job-firestore.svg)](https://pkg.go.dev/github.com/shouni/go-job-firestore)

## 🚀 概要 (About) - 記録・再実行ガード・一覧を、Firestore だけで完結させる

Cloud Tasks へ投入した非同期ジョブの**進行状況を記録し、履歴を一覧する**ための基盤です。保存先は Firestore ひとつで、オブジェクトストレージを必要としません。

「ジョブを投入する → 進行状況を記録する → 履歴をページングして一覧する」という骨格そのものは、生成物が音楽であれ動画であれ漫画であれ同じです。本ライブラリはその共通部分だけを持ち、**成果物そのもののドメインには踏み込みません**。

---

## 🗺 なぜ Firestore か

動機は**原子性ではなく一覧のコスト**です。ジョブの投入は `max_attempts=1` で運用しているため、状態の更新に CAS やトランザクションは要りません。この前提は Firestore にしても変わらないので、本ライブラリは**トランザクションを使いません**。

置き換えたいのは一覧の側です。オブジェクトストレージに状態を置くと、クエリが無いぶん一覧はこうなります。

```
プレフィックス配下を全走査 → 全ジョブ ID をメモリで sort → ページ分を切り出す
→ 1 件ずつ状態ファイルを並行に読む → 重さを短期キャッシュで隠す
```

走査・ソート・キャッシュのいずれも、「クエリが無い」ことの回避策です。Firestore には `Where` / `OrderBy` / `Count` があるので、そのどれも要らなくなります。**その差が、Firestore を持ち込むだけの価値があるか**が、ここで確かめたいことです。

---

## 🏗 プロジェクトレイアウト (Project Layout)

```text
go-job-firestore/
└── jobfirestore/   # 状態の型・記録 (Store / Recorder)・クエリによる一覧 (List)
```

パッケージ名が `firestore` ではなく `jobfirestore` なのは、`cloud.google.com/go/firestore` と衝突させないためです。衝突させると、この SDK を import する全ファイルでエイリアスが要ります。

---

## 📖 使い方 (Usage)

### 1. クライアントを用意する

```go
factory, err := jobfirestore.New(ctx, jobfirestore.WithProjectID(projectID))
if err != nil {
    return err
}
defer func() { _ = factory.Close() }()
```

`Close` は冪等で、以降のアクセサは `ErrClosed` を返します。`WithClient` で生成済みのクライアントを注入した場合、その寿命は**呼び出し元に残ります**（`Close` は閉じません）。閉じる主体が 2 つあると、どちらが所有しているのか呼び出し側から分からなくなるためです。エミュレータ (`FIRESTORE_EMULATOR_HOST`) はクライアント側が解釈するので、本ライブラリ側の設定は要りません。

### 2. 状態の型を定義する

成果物の保存先はサービスごとに形が違うため、共通フィールドだけを `jobfirestore.Status` が持ちます。サービス固有のフィールドは、これを**埋め込んだ**構造体に足してください。

```go
type JobStatus struct {
    jobfirestore.Status
    OutputDir string `json:"output_dir,omitempty" firestore:"output_dir,omitempty"`
}
```

埋め込みは Firestore でも JSON でもフラットに展開されます。**`firestore` タグを省略しないでください。** 省略すると保存されるフィールド名が Go の識別子（`OutputDir`）になり、`json` タグで組み立てた既存のレスポンスやクエリと合わなくなります。

### 3. Store で読み書きする

型引数を取るためパッケージ関数です（Go のメソッドは型引数を取れません）。

```go
store := jobfirestore.NewStore[JobStatus](factory.Client(), "jobs")

// 投入直後に queued を記録する。JobID と UpdatedAt は Save が打刻します
err := store.Save(ctx, jobID, JobStatus{Status: jobfirestore.Status{
    State: jobfirestore.StateQueued, Command: "generate",
}})

status, err := store.Get(ctx, jobID)
```

### 4. Recorder でワーカーから記録する

ワーカーは状態が変わるたびにタスクから状態を組み立て直します。素朴に書くと、再試行のたびに試行回数と投入時刻が失われます。`Recorder` は前回の記録から共通フィールドを引き継いだうえで保存します。

```go
rec := jobfirestore.NewRecorder(store) // store が nil なら記録は行われない

// Cloud Tasks は at-least-once 配信です。ワーカーがエラーを返すと同じタスクが
// 再配信され、生成コストがそのまま二重に発生します。
done, err := rec.Begin(ctx, task.JobID, newStatus(task, jobfirestore.StateRunning),
    func(next, prev *JobStatus) {
        next.Attempts++
        if prev != nil {
            next.OutputDir = prev.OutputDir
        }
    })
if err != nil {
    return err // 状態を読めず判定できない。エラーを返して再配信に委ねる
}
if done {
    return nil // 完了済みの再配信。記録もしない
}

rec.Record(ctx, task.JobID, newStatus(task, jobfirestore.StateSucceeded))
```

引き継ぎの規則です。

| フィールド | 引き継ぎ | 理由 |
| --- | --- | --- |
| `Attempts` / `QueuedAt` | する | 組み立て直しのたびに失わせない |
| `Title` / `Command` | 今回が空のときだけ | 生成の途中で確定した題目を、古い値で上書きしない |
| `State` / `Error` / `UpdatedAt` | しない | 「今回の記録」を表す値。成功後に古い失敗理由が残ってしまう |

`Record` は、完了済みの記録へ `running` / `failed` を書こうとしたときは保存しません。再配信されたタスクは状態を組み立て直すため、そのまま書くと完了したジョブがポーリング中の画面にも再実行ガードにも「まだ終わっていない」と映るからです。**`queued` は例外で、そのまま保存します。** `queued` を書くのは新しい依頼だけなので、同じジョブ ID での作り直しをここで止めてしまわないためです。

---

## 🚦 エラーの分類 (Errors)

呼び出し側が取る判断で分けています。いずれも原因を包んだまま返すので、`errors.Is` の判定はそのままに、ログには元の失敗理由が残ります。

| Firestore が返すもの | 返すエラー | 呼び出し側 |
| --- | --- | --- |
| ドキュメント不在 / `codes.NotFound` | `ErrNotFound` | 404。未記録は正常系なので処理を進めてよい |
| `codes.Unavailable` / `DeadlineExceeded` / `ResourceExhausted` / `Internal` | `ErrUnavailable` | 503。あとで読めるかもしれない |
| `codes.PermissionDenied` / `Unauthenticated` | `ErrUnavailable` | 同上。「無い」と誤認させない |
| ジョブ ID が正規化を通らない | `ErrInvalidJobID` | 400。再試行しても直らない |
| デコード失敗 | そのまま | 500 |

「未記録」と「あるはずなのに読めない」を**別のエラー**にしているのは、両者で取るべき判断が正反対だからです。記録が無いのは正常なので先へ進んでよく、読めなかっただけの場合を「無い」とみなすと、完了済みのジョブを未完了と誤認して生成をまるごとやり直します。

`PermissionDenied` を `ErrNotFound` に寄せないのがとくに要点です。権限設定を間違えた瞬間に、全ジョブが「未記録」に見えます。

再実行ガード (`Begin` / `AlreadySucceeded`) は、未記録 (`ErrNotFound`) だけを「完了していない」として扱います。読めなかった場合 (`ErrUnavailable`) は、「未完了」にも「完了済み」にも倒さずエラーを返します。前者は完了済みのジョブを作り直してガードが防ぐはずのコストを自分で発生させ、後者は未完了のジョブがタスクごと ACK されて二度と実行されないためです。

---

## 📄 ドキュメントの形

コレクション直下に、ドキュメント ID をジョブ ID として 1 ジョブ 1 ドキュメントで置きます。

```text
jobs/{jobID}
```

フィールド名は `json` タグと揃えます（`job_id` / `state` / `queued_at` / …）。同じ構造体がレスポンス JSON にもドキュメントにもなるため、名前が食い違うと画面と保存先で別の語彙を使うことになります。

`updated_at` / `queued_at` は文字列ではなく Firestore の Timestamp として持ちます。文字列だと範囲クエリが辞書順になり、`OrderBy` の意味が形式に依存するためです。

状態は常に最新の 1 世代だけを上書きで保持し、履歴は残しません。

ジョブ ID の正規化は `Store` の内部で必ず行われます。ジョブ ID は URL パスとドキュメント ID の双方に現れるため、検証はセキュリティ境界を兼ねます。呼び出し側で正規化する必要はありません。

---

## 📇 一覧 (Listing)

走査もメモリ上のソートも短期キャッシュも要りません。クエリがその役目を果たします。

```go
items, meta, err := store.List(ctx, page, perPage,
    jobfirestore.WithState(jobfirestore.StateSucceeded),
)
```

ページ番号は 1 始まり、`perPage` が 0 以下のときはページングせず全件を返します。`Total` は全件読み込みではなく `Count` 集計クエリで取ります。

`PageMeta` の JSON タグは、既存サービスが返しているレスポンスと同じ形です。画面と M2M クライアントの双方が依存しているため、変更するときは利用側の追随が要ります。

```go
type PageMeta struct {
    Page       int  `json:"page"`        // 1 始まり。範囲外は最終ページへ丸められる
    PerPage    int  `json:"per_page"`
    Total      int  `json:"total"`
    TotalPages int  `json:"total_pages"`
    HasPrev    bool `json:"has_prev"`
    HasNext    bool `json:"has_next"`
    PrevPage   int  `json:"prev_page"`
    NextPage   int  `json:"next_page"`
    From       int  `json:"from"`        // 「n〜m 件目を表示」の n
    To         int  `json:"to"`          // 同じく m
}
```

---

## 🧭 設計上の約束 (Invariants)

1. **ジョブ ID は必ず正規化してから使う** — URL パスとドキュメント ID の双方に現れるため、検証はセキュリティ境界を兼ねます。
2. **状態は常に最新の 1 世代のみ** — 上書きで更新し、履歴は残しません。
3. **状態の記録に失敗しても生成は止めない** — `Recorder` は保存の失敗を警告ログに留めます。状態は観測のための記録であり、書けなかったことを理由に生成を中断するほうが害が大きいためです。一方、状態を*読めなかった*ときの再実行ガードはエラーを返し、呼び出し側が再配信に委ねられるようにします。
4. **トランザクションを使わない** — `max_attempts=1` で運用しているため、原子性は動機になりません。必要になったのなら、まずその前提が変わっていないかを確かめてください。

---

## 🚧 未決の論点 (Open questions)

確かめたいことそのものなので、決まっていないことを明記します。

1. **ページ番号かカーソルか。** 既存レスポンスの `page` / `total_pages` を保つには `Offset` が要りますが、Firestore は読み飛ばしたドキュメントも課金します。深いページほど走査と変わらないコストになるため、「クエリにしたら安くなる」がどこまで本当かはここで決まります。カーソルへ寄せるなら `PageMeta` の形を変えることになり、画面と M2M クライアントの追随が要ります。
2. **索引の運用。** 複合索引は Terraform（ap-infra）で管理し、スナップショットを更新します。手で足した索引が本番にだけ存在する状態にはしません。
3. **メタデータのキャッシュが要るか。** Firestore の読み取りが十分速ければ、一覧のキャッシュ層を持たずに済みます。持たずに済むなら、それも Firestore へ移る利点のひとつとして数えられます。

---

## 🔗 主な依存関係 (Dependencies)

| パッケージ | 用途 |
| --- | --- |
| [cloud.google.com/go/firestore](https://pkg.go.dev/cloud.google.com/go/firestore) | Firestore クライアント |
| [shouni/go-utils](https://github.com/shouni/go-utils) | ジョブ ID の検証・正規化 (`jobid`) |

`go-utils` だけは残しています。ジョブ ID の正規化はセキュリティ境界を兼ねており、実装をここへ写すと、直すべき場所が 2 つに増えるためです。

---

## 📜 ライセンス (License)

このプロジェクトは [MIT License](https://opensource.org/licenses/MIT) の下で公開されています。

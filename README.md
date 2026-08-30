# 🔥 Go Job Firestore

[![CI](https://github.com/shouni/go-job-firestore/actions/workflows/ci.yml/badge.svg)](https://github.com/shouni/go-job-firestore/actions/workflows/ci.yml)
[![Status](https://img.shields.io/badge/Status-Active-brightgreen)](#)
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

動機は**原子性ではなく一覧のコスト**です。状態の更新に CAS やトランザクションは要りません。本ライブラリは**トランザクションを使いません**。

再試行が有効なキューでも同じです。`PIPELINE_TIMEOUT < dispatch deadline <= Cloud Run timeout` の三段が守られている限り、**アプリが先に諦めて応答を返してから再配信が来る**ので、再試行は直列に届きます。読んでから書く再実行ガードに、同時の競合相手がいません。逆に言えば、**原子性が要るようになるのは再試行回数を増やしたときではなく、この不等式が崩れたとき**です。

置き換えたいのは一覧の側です。オブジェクトストレージに状態を置くと、クエリが無いぶん一覧はこうなります。

```
プレフィックス配下を全走査 → 全ジョブ ID をメモリで sort → ページ分を切り出す
→ 1 件ずつ状態ファイルを並行に読む → 重さを短期キャッシュで隠す
```

走査・ソート・キャッシュのいずれも、「クエリが無い」ことの回避策です。Firestore には `Where` / `OrderBy` / `Count` があるので、そのどれも要らなくなります。**利用側では実際に 3 つとも消えました。** 一覧の走査も、メモリ上の並べ替えも、重さを隠すための短期キャッシュも、移行後に順に削除されています。

---

## 🏗 プロジェクトレイアウト (Project Layout)

```text
go-job-firestore/
└── jobfirestore/   # 状態の型・記録 (Store / Recorder)・クエリによる一覧 (List / Latest)
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

型引数は `Store` 自身に付いています。Go 1.27 からメソッドも型引数を取れますが、**interface のメソッドは今も取れません。** `Recorder` が受ける差し替え口の `StatusStore[T]` は interface なので、型引数を型の側に置く形でしか揃いません。構築は `NewStore` パッケージ関数です。

```go
client, err := factory.Client()
if err != nil {
    return err
}
store := jobfirestore.NewStore[JobStatus](client, serviceName)

// 投入直後に queued を記録する。JobID と UpdatedAt は Save が打刻します
err = store.Save(ctx, jobID, JobStatus{
    State: jobfirestore.StateQueued, Command: "generate",
})

status, err := store.Get(ctx, jobID)
```

埋め込み先のフィールド（`State`・`Command`）を複合リテラルへ直接書けるのは **Go 1.27 以降**です。**利用側の `go.mod` の `go` 行**で決まるので、1.26 以前のままなら `JobStatus{Status: jobfirestore.Status{...}}` と書いてください（ライブラリの動作は変わりません）。

コレクションは**サービスごとに 1 本**で、名前は成果物のバケットと揃えます（「[ドキュメントの形](#-ドキュメントの形)」を参照）。

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
| `codes.Unavailable` / `DeadlineExceeded` / `ResourceExhausted` / `Internal` / `Aborted` | `ErrUnavailable` | 503。あとで読めるかもしれない |
| `codes.PermissionDenied` / `Unauthenticated` | `ErrUnavailable` | 同上。「無い」と誤認させない |
| ジョブ ID が正規化を通らない | `ErrInvalidJobID` | 400。再試行しても直らない |
| デコード失敗 | そのまま | 500 |

「未記録」と「あるはずなのに読めない」を**別のエラー**にしているのは、両者で取るべき判断が正反対だからです。記録が無いのは正常なので先へ進んでよく、読めなかった場合は判断を保留するしかありません。

`PermissionDenied` を `ErrNotFound` に寄せないのがとくに要点です。権限設定を間違えた瞬間に、全ジョブが「未記録」に見えます。

再実行ガード (`Begin` / `AlreadySucceeded`) は、未記録 (`ErrNotFound`) だけを「完了していない」として扱います。読めなかった場合 (`ErrUnavailable`) は、「未完了」にも「完了済み」にも倒さずエラーを返します。前者は完了済みのジョブを作り直してガードが防ぐはずのコストを自分で発生させ、後者は未完了のジョブがタスクごと ACK されて二度と実行されないためです。

---

## 📄 ドキュメントの形

コレクション直下に、ドキュメント ID をジョブ ID として 1 ジョブ 1 ドキュメントで置きます。**コレクションはサービスごとに 1 本**で、名前は成果物のバケットと同じ語彙にします。

```text
{serviceName}/{jobID}
```

1 本の共有コレクションに判別フィールドを持たせる形は採りません。全クエリがサービスでの絞り込みを要求することになり、**忘れても落ちずに、他サービスのジョブが履歴画面へ静かに混ざる**からです。複合索引もサービスごとに独立するので、片方の変更が他方に影響しません。

代償として、フリート横断の一覧は組めません（コレクション ID が違うため collection group query では束ねられません）。いま要求は無く、ジョブ状態は観測のための使い捨てデータで本体は成果物のほうなので、必要になった時点で書き直せます。

フィールド名は `json` タグと揃えます（`job_id` / `state` / `queued_at` / …）。同じ構造体がレスポンス JSON にもドキュメントにもなるため、名前が食い違うと画面と保存先で別の語彙を使うことになります。

`updated_at` / `queued_at` は文字列ではなく Firestore の Timestamp として持ちます。文字列だと範囲クエリが辞書順になり、`OrderBy` の意味が形式に依存するためです。

### 成果物を消しても状態は残ります

成果物と同じ場所に状態ファイルを置く作りなら、履歴削除（プレフィックスの一括削除）で状態も一緒に片付きます。**ドキュメントは別の場所にあるので、その連動はありません。** 放置すると孤児が溜まり続けます。

手当ては 2 つで、**どちらも利用側の仕事です**。

- 履歴を削除するときに `Store.Delete` を呼ぶ
- `updated_at` に TTL ポリシーを張って期限切れを自動削除する（Terraform の `google_firestore_field`）

ジョブ ID の正規化は `Store` の内部で必ず行われるので、呼び出し側で正規化する必要はありません（理由は「[設計上の約束](#-設計上の約束-invariants)」を参照）。

---

## 📇 一覧 (Listing)

走査もメモリ上のソートも短期キャッシュも要りません。クエリがその役目を果たします。

```go
items, meta, err := store.List(ctx, page, perPage,
    jobfirestore.WithState(jobfirestore.StateSucceeded),
)
```

絞り込みは 3 種類です。

| オプション | 絞り込む対象 | 備考 |
|---|---|---|
| `WithState` | `state` | 状態はどのサービスでも同じ意味を持つ |
| `WithCommand` | `command` | 可変長。2 つ以上渡すと `in` 検索（30 個まで）。1 つの一覧に複数のコマンドが対応することがある |
| `WithField` | サービス固有のフィールド | 等値のみ。`path` は `firestore` タグの名前 |

`WithField` が要るのは、サービス固有のフィールドで絞れないと、利用側が全件を読んでメモリで落とすことになり、一覧を Firestore へ移した意味が半分消えるからです。等値だけなのは、不等号を許すと Firestore が**そのフィールドを並べ替えの先頭に要求する**ため、`WithOrderBy` と組み合わせたときに黙って別の並び順になるからです。

```go
// 「タイトル未合成の画像ジョブだけ」を新しい順に
items, meta, err := store.List(ctx, page, perPage,
    jobfirestore.WithCommand("generate_image_from_recipe"),
    jobfirestore.WithField("title_applied", false),
)
```

ページ番号は 1 始まり、`perPage` が 0 以下のときはページングせず全件を返します。`Total` は全件読み込みではなく `Count` 集計クエリで取ります。

### ページ送りが要らないとき

トップ画面の抜粋のように「最新の数件」だけが要るなら `Latest` を使います。件数集計をしないので、`List` より 1 往復ぶん安く済みます。

```go
// 最新 6 件。ページ情報は返らない
items, err := store.Latest(ctx, 6, jobfirestore.WithCommand("compose"))
```

`PageMeta` を返さないのは意図的です。総件数を知らないままページ情報を組み立てると `Total` と `TotalPages` に 0 か当てずっぽうを入れることになり、受け取った側は「本当に 0 件」なのか「数えていない」のかを区別できません。**ページ送りが要るなら `List`、要らないなら `Latest`** という分け方にしてあります。

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
4. **トランザクションを使わない** — 三段のタイムアウトが再配信を直列に保つため、原子性は動機になりません（「[なぜ Firestore か](#-なぜ-firestore-か)」を参照）。必要になったのなら、まずその不等式が崩れていないかを確かめてください。再試行回数だけを見て判断しないこと。

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

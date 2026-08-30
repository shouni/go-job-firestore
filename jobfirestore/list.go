package jobfirestore

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/iterator"
)

// countAlias は件数集計クエリの結果を取り出すためのキーです。
const countAlias = "all"

// listOptions は List の絞り込みと並び順です。
type listOptions struct {
	state      State
	command    string
	orderBy    string
	descending bool
}

// ListOption は List の挙動を変更します。
type ListOption func(*listOptions)

// WithState は、指定した状態のジョブだけを一覧します。
func WithState(state State) ListOption {
	return func(o *listOptions) { o.state = state }
}

// WithCommand は、指定したコマンドのジョブだけを一覧します。
func WithCommand(command string) ListOption {
	return func(o *listOptions) { o.command = command }
}

// WithOrderBy は並べ替えるフィールドと向きを変えます。
// 既定は queued_at の降順（新しい順）です。
//
// 絞り込みと組み合わせると複合索引が要ります。索引は Terraform などコードの側で
// 管理してください（手で足した索引が本番にだけ存在する状態にしないためです）。
// 絞り込みを付けない並べ替えだけなら、単一フィールドの自動索引で足ります。
func WithOrderBy(field string, descending bool) ListOption {
	return func(o *listOptions) {
		o.orderBy = field
		o.descending = descending
	}
}

// List は、ジョブ状態を新しい順に 1 ページ分返します。
//
// ページ番号は 1 始まり、perPage が 0 以下のときはページングせず全件を返します。
// Total は全件の読み込みではなく件数集計クエリで求めるため、ページの外にある
// ドキュメントは読みません。
//
// デコードに失敗したドキュメントはエラーとして返します。一覧から黙って落とすと、
// 壊れた記録があることに誰も気づきません。
func (s *Store[T]) List(ctx context.Context, page, perPage int, opts ...ListOption) ([]T, PageMeta, error) {
	cfg := listOptions{orderBy: "queued_at", descending: true}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	if s.client == nil {
		return nil, PageMeta{}, errors.New("jobfirestore: client is not configured")
	}
	if s.collection == "" {
		return nil, PageMeta{}, errors.New("jobfirestore: collection is not configured")
	}

	query := s.client.Collection(s.collection).Query
	if cfg.state != "" {
		query = query.Where("state", "==", string(cfg.state))
	}
	if cfg.command != "" {
		query = query.Where("command", "==", cfg.command)
	}

	total, err := count(ctx, query)
	if err != nil {
		return nil, PageMeta{}, err
	}

	meta := newPageMeta(page, perPage, total)
	if total == 0 {
		return nil, meta, nil
	}

	direction := firestore.Asc
	if cfg.descending {
		direction = firestore.Desc
	}
	query = query.OrderBy(cfg.orderBy, direction)
	if meta.PerPage > 0 {
		query = query.Offset(meta.offset()).Limit(meta.PerPage)
	}

	items, err := collect[T](ctx, query)
	if err != nil {
		return nil, meta, err
	}
	return items, meta, nil
}

// collect はクエリの結果を T へデコードして集めます。
func collect[T any](ctx context.Context, query firestore.Query) ([]T, error) {
	iter := query.Documents(ctx)
	defer iter.Stop()

	var items []T
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return items, nil
		}
		if err != nil {
			return nil, classify("list", err)
		}

		var item T
		if err := snap.DataTo(&item); err != nil {
			return nil, fmt.Errorf("ジョブ状態のデコードに失敗しました (%s): %w", snap.Ref.ID, err)
		}
		if e, ok := any(&item).(interface{ EnsureJobID(string) }); ok {
			e.EnsureJobID(snap.Ref.ID)
		}
		items = append(items, item)
	}
}

// count は、絞り込みだけを適用したクエリの総件数を返します。
//
// ドキュメント本体は読まないため、一覧全体の件数を得るためにページの外を
// 走査する必要がありません。オブジェクトストレージ上の一覧が全走査を強いられて
// いたのは、これに相当する手段が無かったためです。
func count(ctx context.Context, query firestore.Query) (int, error) {
	result, err := query.NewAggregationQuery().WithCount(countAlias).Get(ctx)
	if err != nil {
		return 0, classify("count", err)
	}

	value, ok := result[countAlias]
	if !ok {
		return 0, errors.New("jobfirestore: 件数集計クエリが結果を返しませんでした")
	}
	counted, ok := value.(*firestorepb.Value)
	if !ok {
		return 0, fmt.Errorf("jobfirestore: 件数集計クエリが想定外の型を返しました: %T", value)
	}
	return int(counted.GetIntegerValue()), nil
}

package jobfirestore

import (
	"slices"
	"testing"
)

// TestNewListOptions は、絞り込みの組み立てを固定します。
//
// List そのものはエミュレータが要るので、ここで確かめるのは Firestore へ渡る前の形です。
func TestNewListOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []ListOption
		want listOptions
	}{
		{
			name: "既定は queued_at の降順",
			want: listOptions{orderBy: "queued_at", descending: true},
		},
		{
			// 1 つの一覧に複数のコマンドが対応することがある。
			name: "コマンドは重ねられる",
			opts: []ListOption{WithCommand("compose"), WithCommand("generate_from_recipe")},
			want: listOptions{
				commands: []string{"compose", "generate_from_recipe"},
				orderBy:  "queued_at", descending: true,
			},
		},
		{
			// 空を通すと Where("command", "==", "") になり、一覧が全部消える。
			name: "空のコマンドは無視する",
			opts: []ListOption{WithCommand("compose", "", "generate_from_recipe")},
			want: listOptions{
				commands: []string{"compose", "generate_from_recipe"},
				orderBy:  "queued_at", descending: true,
			},
		},
		{
			name: "サービス固有のフィールドは複数指定できる",
			opts: []ListOption{WithField("title_applied", false), WithField("visual_mode", "anime")},
			want: listOptions{
				fields:  []fieldFilter{{path: "title_applied", value: false}, {path: "visual_mode", value: "anime"}},
				orderBy: "queued_at", descending: true,
			},
		},
		{
			// パスが空だと Firestore がエラーを返す。オプション側で落とす。
			name: "空のパスは無視する",
			opts: []ListOption{WithField("", true)},
			want: listOptions{orderBy: "queued_at", descending: true},
		},
		{
			name: "状態と並び順は上書きできる",
			opts: []ListOption{WithState(StateFailed), WithOrderBy("updated_at", false)},
			want: listOptions{state: StateFailed, orderBy: "updated_at"},
		},
		{
			name: "nil のオプションは読み飛ばす",
			opts: []ListOption{nil, WithState(StateQueued)},
			want: listOptions{state: StateQueued, orderBy: "queued_at", descending: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := newListOptions(tt.opts)

			if got.state != tt.want.state {
				t.Errorf("state = %q, want %q", got.state, tt.want.state)
			}
			if got.orderBy != tt.want.orderBy || got.descending != tt.want.descending {
				t.Errorf("並び順 = (%q, %t), want (%q, %t)",
					got.orderBy, got.descending, tt.want.orderBy, tt.want.descending)
			}
			if !slices.Equal(got.commands, tt.want.commands) {
				t.Errorf("commands = %v, want %v", got.commands, tt.want.commands)
			}
			if !slices.Equal(got.fields, tt.want.fields) {
				t.Errorf("fields = %v, want %v", got.fields, tt.want.fields)
			}
		})
	}
}

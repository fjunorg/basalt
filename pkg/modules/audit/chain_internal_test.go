package audit

import (
	"context"
	"testing"

	"github.com/basalt-go/basalt/pkg/kernel/crypto"
)

func newTestChain() *Chain {
	// 匿名嵌入 Provider 恰好满足 Hasher 接口（Hash + Algorithm）。
	return NewChain(struct{ crypto.Provider }{crypto.Default()})
}

func TestChainAppendAndVerify(t *testing.T) {
	c := newTestChain()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := c.Append(ctx, Event{Module: "test", Event: "e"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if brk, err := c.Verify(ctx); err != nil || brk != 0 {
		t.Fatalf("verify clean chain: brk=%d err=%v", brk, err)
	}
	events, _ := c.List(ctx, "", 0)
	if len(events) != 5 {
		t.Fatalf("events = %d, want 5", len(events))
	}
	// List 倒序（最新在前）：最老一条 PrevHash 为空，其余指向前条哈希。
	if events[len(events)-1].PrevHash != "" || events[0].PrevHash != events[1].Hash {
		t.Fatal("chain linkage broken")
	}
}

func TestChainDetectsTampering(t *testing.T) {
	c := newTestChain()
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_ = c.Append(ctx, Event{Module: "m", Event: "e"})
	}

	// 模拟存储层记录被篡改：直接修改第 2 条的内容字段。
	c.events[1].Event = "hacked"
	if brk, err := c.Verify(ctx); err == nil || brk != 2 {
		t.Fatalf("tampered content: brk=%d err=%v, want brk=2", brk, err)
	}
}

func TestChainDetectsDeletion(t *testing.T) {
	c := newTestChain()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = c.Append(ctx, Event{Module: "m", Event: "e"})
	}

	// 模拟删除第 3 条：PrevHash 链接断开。
	c.events = append(c.events[:2], c.events[3:]...)
	if brk, err := c.Verify(ctx); err == nil || brk != 3 {
		t.Fatalf("deleted record: brk=%d err=%v, want brk=3", brk, err)
	}
}

func TestListFilterAndLimit(t *testing.T) {
	c := newTestChain()
	ctx := context.Background()
	_ = c.Append(ctx, Event{Module: "a", Event: "e1"})
	_ = c.Append(ctx, Event{Module: "b", Event: "e2"})
	_ = c.Append(ctx, Event{Module: "a", Event: "e3"})

	only, _ := c.List(ctx, "a", 0)
	if len(only) != 2 {
		t.Fatalf("module filter = %d, want 2", len(only))
	}
	// List 倒序返回（最新在前）
	if only[0].Event != "e3" {
		t.Fatalf("newest first: %q", only[0].Event)
	}
	limited, _ := c.List(ctx, "", 1)
	if len(limited) != 1 {
		t.Fatalf("limit = %d, want 1", len(limited))
	}
}

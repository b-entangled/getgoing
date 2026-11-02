package flyweight

import (
	"fmt"
	"sync"
	"testing"
)

func tex(name string, bytes int) *TextureData {
	return &TextureData{Name: name, Width: 1024, Height: 1024, Pixels: make([]byte, bytes)}
}

func TestInternReturnsSamePointer(t *testing.T) {
	a := NewAtlas()
	t1, err := a.Intern("hero", func() *TextureData { return tex("hero", 1024) })
	if err != nil {
		t.Fatal(err)
	}
	t2, err := a.Intern("hero", func() *TextureData { return tex("hero", 1024) })
	if err != nil {
		t.Fatal(err)
	}
	if t1 != t2 {
		t.Fatal("same key produced different pointers — flyweight not sharing")
	}
}

func TestInternBuildsOnlyOnce(t *testing.T) {
	a := NewAtlas()
	builds := 0
	build := func() *TextureData {
		builds++
		return tex("x", 10)
	}
	for range 100 {
		if _, err := a.Intern("x", build); err != nil {
			t.Fatal(err)
		}
	}
	if builds != 1 {
		t.Fatalf("want exactly 1 build, got %d", builds)
	}
	hits, misses, pooled := a.Stats()
	if hits != 99 || misses != 1 || pooled != 1 {
		t.Fatalf("stats: hits=%d misses=%d pooled=%d", hits, misses, pooled)
	}
}

func TestDifferentKeysAreDistinct(t *testing.T) {
	a := NewAtlas()
	x, _ := a.Intern("a", func() *TextureData { return tex("a", 1) })
	y, _ := a.Intern("b", func() *TextureData { return tex("b", 1) })
	if x == y {
		t.Fatal("distinct keys shared a texture")
	}
	if a.Pooled() != 2 {
		t.Fatalf("want 2 pooled, got %d", a.Pooled())
	}
}

func TestSpritesShareIntrinsicState(t *testing.T) {
	a := NewAtlas()
	sp := NewSpawner(a)
	build := func() *TextureData { return tex("bullet", 4096) }

	var sprites []*Sprite
	for i := range 1000 {
		s, err := sp.Spawn("bullet", build, float64(i), float64(i*2))
		if err != nil {
			t.Fatal(err)
		}
		sprites = append(sprites, s)
	}

	// 1000 sprites, one texture.
	if a.Pooled() != 1 {
		t.Fatalf("want 1 pooled texture, got %d", a.Pooled())
	}
	first := sprites[0].Tex
	for i, s := range sprites {
		if s.Tex != first {
			t.Fatalf("sprite %d has a distinct texture", i)
		}
	}
	// Extrinsic state still differs per sprite.
	if sprites[0].X == sprites[999].X {
		t.Fatal("extrinsic state not independent")
	}
}

func TestMemorySavingIsReal(t *testing.T) {
	const per = 1 << 20 // 1 MiB per texture
	a := NewAtlas()
	build := func() *TextureData { return tex("big", per) }

	const n = 500
	for i := range n {
		_, err := a.Intern(fmt.Sprintf("t%d", i%3), build) // only 3 distinct keys
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, pooled := a.Stats()
	if pooled != 3 {
		t.Fatalf("want 3 distinct textures, got %d", pooled)
	}
	// 500 logical textures at 1MiB each would be 500MiB; we hold 3MiB.
	if int64(pooled)*per != 3*per {
		t.Fatal("math")
	}
}

func TestConcurrentInternIsRaceFree(t *testing.T) {
	a := NewAtlas()
	var wg sync.WaitGroup
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				key := fmt.Sprintf("k%d", i%5)
				if _, err := a.Intern(key, func() *TextureData { return tex(key, 8) }); err != nil {
					t.Error(err)
					return
				}
			}
			_ = g
		}()
	}
	wg.Wait()
	if a.Pooled() > 5 {
		t.Fatalf("want at most 5 textures, got %d", a.Pooled())
	}
}

func TestMissingKeyWithoutBuilderErrors(t *testing.T) {
	a := NewAtlas()
	if _, err := a.Intern("nope", nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestNilBuilderResultErrors(t *testing.T) {
	a := NewAtlas()
	if _, err := a.Intern("k", func() *TextureData { return nil }); err == nil {
		t.Fatal("expected error")
	}
}

// --- bounded atlas ---

func TestBoundedAtlasEvictsLRU(t *testing.T) {
	b := NewBoundedAtlas(2)
	build := func() *TextureData { return tex("t", 1) }

	_, _ = b.Intern("a", build)
	_, _ = b.Intern("b", build)
	if b.Pooled() != 2 {
		t.Fatalf("want 2, got %d", b.Pooled())
	}

	// Touch "a" so "b" becomes least-recently-used.
	_, _ = b.Intern("a", build)
	_, _ = b.Intern("c", build) // evicts b

	if b.Pooled() != 2 {
		t.Fatalf("want 2 after eviction, got %d", b.Pooled())
	}
	if b.Builds() != 3 {
		t.Fatalf("want 3 builds, got %d", b.Builds())
	}

	// Re-requesting "b" must rebuild it.
	_, _ = b.Intern("b", build)
	if b.Builds() != 4 {
		t.Fatalf("evicted key should rebuild, builds=%d", b.Builds())
	}
}

func TestBoundedAtlasNeverExceedsCapacity(t *testing.T) {
	b := NewBoundedAtlas(4)
	build := func() *TextureData { return tex("t", 1) }
	for i := range 100 {
		_, _ = b.Intern(fmt.Sprintf("k%d", i), build)
	}
	if b.Pooled() > 4 {
		t.Fatalf("capacity violated: %d", b.Pooled())
	}
}

func TestBoundedAtlasMinimumCapacity(t *testing.T) {
	b := NewBoundedAtlas(0)
	if _, err := b.Intern("k", func() *TextureData { return tex("t", 1) }); err != nil {
		t.Fatal(err)
	}
	if b.Pooled() != 1 {
		t.Fatalf("got %d", b.Pooled())
	}
}

func TestBoundedConcurrentAccess(t *testing.T) {
	b := NewBoundedAtlas(8)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				k := fmt.Sprintf("k%d", i%20)
				if _, err := b.Intern(k, func() *TextureData { return tex(k, 4) }); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if b.Pooled() > 8 {
		t.Fatalf("capacity violated under concurrency: %d", b.Pooled())
	}
}

package workflow

import (
	"testing"
	"time"
)

func TestNextCronRun_EveryMinute(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC).UnixMilli()
	next, err := NextCronRun("* * * * *", "", now)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	want := time.Date(2026, 3, 1, 10, 31, 0, 0, time.UTC).UnixMilli()
	if next != want {
		t.Errorf("got %d, want %d", next, want)
	}
}

func TestNextCronRun_SpecificTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC).UnixMilli()
	next, err := NextCronRun("0 9 * * *", "UTC", now)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	got := time.UnixMilli(next).UTC()
	if got.Hour() != 9 || got.Minute() != 0 {
		t.Errorf("got %s, want 09:00", got.Format("15:04"))
	}
}

func TestNextCronRun_WithTimezone(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC).UnixMilli()
	next, err := NextCronRun("0 9 * * *", "Europe/Moscow", now)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	msk, _ := time.LoadLocation("Europe/Moscow")
	got := time.UnixMilli(next).In(msk)
	if got.Hour() != 9 {
		t.Errorf("got %s MSK, want 09:00", got.Format("15:04"))
	}
}

func TestNextCronRun_DayOfWeek(t *testing.T) {
	t.Parallel()
	// 2026-03-01 is Sunday (0)
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).UnixMilli()
	next, err := NextCronRun("0 8 * * 1", "", now) // Monday
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	got := time.UnixMilli(next).UTC()
	if got.Weekday() != time.Monday {
		t.Errorf("got %s, want Monday", got.Weekday())
	}
}

func TestNextCronRun_Step(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	next, err := NextCronRun("*/15 * * * *", "", now)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	got := time.UnixMilli(next).UTC()
	if got.Minute()%15 != 0 {
		t.Errorf("minute = %d, want multiple of 15", got.Minute())
	}
}

func TestNextCronRun_InvalidExpr(t *testing.T) {
	t.Parallel()
	_, err := NextCronRun("bad", "", time.Now().UnixMilli())
	if err == nil {
		t.Error("expected error for invalid expression")
	}
}

func TestNextCronRun_InvalidTimezone(t *testing.T) {
	t.Parallel()
	_, err := NextCronRun("* * * * *", "Nonexistent/Zone", time.Now().UnixMilli())
	if err == nil {
		t.Error("expected error for invalid timezone")
	}
}

func TestParseCronField_Star(t *testing.T) {
	t.Parallel()
	m, err := parseCronField("*", 0, 59)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 60 {
		t.Errorf("got %d values, want 60", len(m))
	}
}

func TestParseCronField_Range(t *testing.T) {
	t.Parallel()
	m, err := parseCronField("1-5", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if !m[i] {
			t.Errorf("missing %d", i)
		}
	}
}

func TestParseCronField_StepWithStar(t *testing.T) {
	t.Parallel()
	m, err := parseCronField("*/10", 0, 59)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{0, 10, 20, 30, 40, 50}
	for _, v := range want {
		if !m[v] {
			t.Errorf("missing %d", v)
		}
	}
}

func TestParseCronField_Comma(t *testing.T) {
	t.Parallel()
	m, err := parseCronField("1,3,5", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Errorf("got %d values, want 3", len(m))
	}
}

func TestParseCronField_Invalid(t *testing.T) {
	t.Parallel()
	_, err := parseCronField("abc", 0, 59)
	if err == nil {
		t.Error("expected error")
	}
}

func TestParseCronField_Empty(t *testing.T) {
	t.Parallel()
	_, err := parseCronField("", 0, 59)
	if err == nil {
		t.Error("expected error for empty field")
	}
}

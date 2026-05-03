package claw

import (
	"testing"
	"time"
)

func TestNextCronTimeEvery(t *testing.T) {
	from := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	got, err := nextCronTime("@every 30m", from, time.UTC)
	if err != nil {
		t.Fatalf("@every 30m: %v", err)
	}
	want := from.Add(30 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("@every 30m got %v want %v", got, want)
	}
}

func TestNextCronTimeAtDailyAdvancesPastNow(t *testing.T) {
	loc := time.UTC
	// from = 14:00, "@at 09:00" must advance to next day 09:00.
	from := time.Date(2026, 5, 3, 14, 0, 0, 0, loc)
	got, err := nextCronTime("@at 09:00", from, loc)
	if err != nil {
		t.Fatalf("@at 09:00: %v", err)
	}
	want := time.Date(2026, 5, 4, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("@at 09:00 got %v want %v", got, want)
	}
	// from = 08:00, "@at 09:00" must fire today at 09:00.
	from2 := time.Date(2026, 5, 3, 8, 0, 0, 0, loc)
	got2, err := nextCronTime("@at 09:00", from2, loc)
	if err != nil {
		t.Fatalf("@at 09:00: %v", err)
	}
	want2 := time.Date(2026, 5, 3, 9, 0, 0, 0, loc)
	if !got2.Equal(want2) {
		t.Fatalf("@at 09:00 same day got %v want %v", got2, want2)
	}
}

func TestNextCronTimeDayOfWeek(t *testing.T) {
	// 2026-05-03 is a Sunday. "@dow Mon 09:00" → 2026-05-04 09:00.
	from := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	got, err := nextCronTime("@dow Mon 09:00", from, time.UTC)
	if err != nil {
		t.Fatalf("@dow Mon 09:00: %v", err)
	}
	want := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("@dow Mon 09:00 got %v want %v", got, want)
	}
}

func TestNextCronTimeFiveField(t *testing.T) {
	loc := time.UTC
	from := time.Date(2026, 5, 3, 14, 30, 0, 0, loc)
	// "0 9 * * *" — every day at 09:00 → next should be tomorrow 09:00.
	got, err := nextCronTime("0 9 * * *", from, loc)
	if err != nil {
		t.Fatalf("cron expr: %v", err)
	}
	want := time.Date(2026, 5, 4, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("0 9 * * * got %v want %v", got, want)
	}
}

func TestNextCronTimeFiveFieldWithRange(t *testing.T) {
	loc := time.UTC
	from := time.Date(2026, 5, 3, 12, 30, 0, 0, loc) // Sunday
	// "0 9 * * 1-5" — weekdays at 09:00 → Monday 09:00.
	got, err := nextCronTime("0 9 * * 1-5", from, loc)
	if err != nil {
		t.Fatalf("cron expr: %v", err)
	}
	want := time.Date(2026, 5, 4, 9, 0, 0, 0, loc) // Monday
	if !got.Equal(want) {
		t.Fatalf("0 9 * * 1-5 got %v want %v", got, want)
	}
}

func TestNextCronTimeRespectsTimezone(t *testing.T) {
	loc, err := time.LoadLocation("America/Bogota")
	if err != nil {
		t.Skipf("timezone db missing: %v", err)
	}
	// 12:00 UTC = 07:00 Bogotá. "@at 09:00" in Bogotá → 14:00 UTC same day.
	from := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	got, err := nextCronTime("@at 09:00", from, loc)
	if err != nil {
		t.Fatalf("@at 09:00 with tz: %v", err)
	}
	want := time.Date(2026, 5, 3, 14, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("@at 09:00 Bogotá got %v want %v", got, want)
	}
}

func TestNextCronTimeRejectsUnknown(t *testing.T) {
	_, err := nextCronTime("never", time.Now(), time.UTC)
	if err == nil {
		t.Fatal("expected error for unknown spec")
	}
}

func TestUserLocationFallsBackToUTC(t *testing.T) {
	state := State{}
	state.User.Timezone = ""
	if loc := userLocation(state); loc != time.UTC {
		t.Fatalf("empty tz should be UTC, got %v", loc)
	}
	state.User.Timezone = "Bogus/Timezone"
	if loc := userLocation(state); loc != time.UTC {
		t.Fatalf("bad tz should fall back to UTC, got %v", loc)
	}
}

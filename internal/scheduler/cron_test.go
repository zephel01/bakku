package scheduler

import (
	"reflect"
	"testing"
)

func TestValidateCron(t *testing.T) {
	valid := []string{"0 3 * * *", "*/15 * * * *", "30 2 * * 1", "0 0 1 * *", "0 9-17 * * mon-fri"}
	for _, e := range valid {
		if err := ValidateCron(e); err != nil {
			t.Errorf("ValidateCron(%q) unexpected error: %v", e, err)
		}
	}
	invalid := []string{"", "not a cron", "60 * * * *", "* * * * * *"}
	for _, e := range invalid {
		if err := ValidateCron(e); err == nil {
			t.Errorf("ValidateCron(%q) expected an error, got nil", e)
		}
	}
}

func TestValidateName(t *testing.T) {
	if err := ValidateName("daily-backup_1"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	for _, bad := range []string{"", "has space", "slash/es", "semi;colon"} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) expected error, got nil", bad)
		}
	}
}

func TestToOnCalendarDailyAtFixedTime(t *testing.T) {
	got, err := ToOnCalendar("0 3 * * *")
	if err != nil {
		t.Fatalf("ToOnCalendar: %v", err)
	}
	want := "*-*-* 3:0:00"
	if got != want {
		t.Errorf("ToOnCalendar(\"0 3 * * *\") = %q, want %q", got, want)
	}
}

func TestToOnCalendarWeeklyOnWeekday(t *testing.T) {
	got, err := ToOnCalendar("30 2 * * 1")
	if err != nil {
		t.Fatalf("ToOnCalendar: %v", err)
	}
	want := "Mon *-*-* 2:30:00"
	if got != want {
		t.Errorf("ToOnCalendar(\"30 2 * * 1\") = %q, want %q", got, want)
	}
}

func TestToOnCalendarStepMinutes(t *testing.T) {
	got, err := ToOnCalendar("*/15 * * * *")
	if err != nil {
		t.Fatalf("ToOnCalendar: %v", err)
	}
	want := "*-*-* *:0,15,30,45:00"
	if got != want {
		t.Errorf("ToOnCalendar(\"*/15 * * * *\") = %q, want %q", got, want)
	}
}

func TestToOnCalendarEveryMinuteEveryHour(t *testing.T) {
	got, err := ToOnCalendar("* * * * *")
	if err != nil {
		t.Fatalf("ToOnCalendar: %v", err)
	}
	want := "*-*-* *:*:00"
	if got != want {
		t.Errorf("ToOnCalendar(\"* * * * *\") = %q, want %q", got, want)
	}
}

func TestToOnCalendarDayOfMonth(t *testing.T) {
	got, err := ToOnCalendar("0 0 1 * *")
	if err != nil {
		t.Fatalf("ToOnCalendar: %v", err)
	}
	want := "*-*-1 0:0:00"
	if got != want {
		t.Errorf("ToOnCalendar(\"0 0 1 * *\") = %q, want %q", got, want)
	}
}

func TestToOnCalendarWeekdayRange(t *testing.T) {
	got, err := ToOnCalendar("0 9 * * mon-fri")
	if err != nil {
		t.Fatalf("ToOnCalendar: %v", err)
	}
	want := "Mon,Tue,Wed,Thu,Fri *-*-* 9:0:00"
	if got != want {
		t.Errorf("ToOnCalendar(\"0 9 * * mon-fri\") = %q, want %q", got, want)
	}
}

func TestToOnCalendarInvalidExpr(t *testing.T) {
	if _, err := ToOnCalendar("not a cron"); err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestToStartCalendarIntervalDailyAtFixedTime(t *testing.T) {
	got, err := ToStartCalendarInterval("0 3 * * *")
	if err != nil {
		t.Fatalf("ToStartCalendarInterval: %v", err)
	}
	want := []LaunchdCalendarInterval{
		{Minute: 0, HasMinute: true, Hour: 3, HasHour: true, Day: -1, Weekday: -1, Month: -1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToStartCalendarInterval(\"0 3 * * *\") = %+v, want %+v", got, want)
	}
}

func TestToStartCalendarIntervalWeekly(t *testing.T) {
	got, err := ToStartCalendarInterval("30 2 * * 1")
	if err != nil {
		t.Fatalf("ToStartCalendarInterval: %v", err)
	}
	want := []LaunchdCalendarInterval{
		{Minute: 30, HasMinute: true, Hour: 2, HasHour: true, Day: -1, Weekday: 1, HasWeekday: true, Month: -1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToStartCalendarInterval(\"30 2 * * 1\") = %+v, want %+v", got, want)
	}
}

func TestToStartCalendarIntervalDayOfMonth(t *testing.T) {
	got, err := ToStartCalendarInterval("0 0 1 * *")
	if err != nil {
		t.Fatalf("ToStartCalendarInterval: %v", err)
	}
	want := []LaunchdCalendarInterval{
		{Minute: 0, HasMinute: true, Hour: 0, HasHour: true, Day: 1, HasDay: true, Weekday: -1, Month: -1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToStartCalendarInterval(\"0 0 1 * *\") = %+v, want %+v", got, want)
	}
}

func TestToStartCalendarIntervalCartesianExpansion(t *testing.T) {
	// Two hours x two minutes -> 4 combinations (order: hour outer loop
	// currently iterates months->doms->dows->hours->minutes, so with those
	// three unconstrained we just get hour-major, minute-minor ordering).
	got, err := ToStartCalendarInterval("0,30 9,17 * * *")
	if err != nil {
		t.Fatalf("ToStartCalendarInterval: %v", err)
	}
	want := []LaunchdCalendarInterval{
		{Minute: 0, HasMinute: true, Hour: 9, HasHour: true, Day: -1, Weekday: -1, Month: -1},
		{Minute: 30, HasMinute: true, Hour: 9, HasHour: true, Day: -1, Weekday: -1, Month: -1},
		{Minute: 0, HasMinute: true, Hour: 17, HasHour: true, Day: -1, Weekday: -1, Month: -1},
		{Minute: 30, HasMinute: true, Hour: 17, HasHour: true, Day: -1, Weekday: -1, Month: -1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToStartCalendarInterval(\"0,30 9,17 * * *\") = %+v, want %+v", got, want)
	}
}

func TestToStartCalendarIntervalInvalidExpr(t *testing.T) {
	if _, err := ToStartCalendarInterval("garbage"); err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestExpandFieldStepWithRange(t *testing.T) {
	got, err := expandField("1-10/3", 0, 59, nil)
	if err != nil {
		t.Fatalf("expandField: %v", err)
	}
	want := []int{1, 4, 7, 10}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expandField(\"1-10/3\") = %v, want %v", got, want)
	}
}

func TestExpandFieldList(t *testing.T) {
	got, err := expandField("5,1,3", 0, 59, nil)
	if err != nil {
		t.Fatalf("expandField: %v", err)
	}
	want := []int{1, 3, 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expandField(\"5,1,3\") = %v, want %v", got, want)
	}
}

func TestExpandFieldOutOfRange(t *testing.T) {
	if _, err := expandField("99", 0, 59, nil); err == nil {
		t.Fatal("expected out-of-range error")
	}
}

func TestExpandFieldNames(t *testing.T) {
	got, err := expandField("mon-fri", 0, 6, dowNames)
	if err != nil {
		t.Fatalf("expandField: %v", err)
	}
	want := []int{1, 2, 3, 4, 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expandField(\"mon-fri\") = %v, want %v", got, want)
	}
}

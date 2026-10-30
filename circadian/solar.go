package circadian

import (
	"math"
	"time"
)

// solarTime uses the NOAA fractional-year equation and a 90.833-degree zenith.
// It estimates apparent sunrise/sunset; terrain and weather are not modelled.
func solarTime(date time.Time, latitude, longitude float64, sunrise bool) (time.Time, bool) {
	radians := math.Pi / 180
	days := 365.0
	if date.Year()%4 == 0 && (date.Year()%100 != 0 || date.Year()%400 == 0) {
		days = 366
	}
	gamma := 2 * math.Pi / days * (float64(date.YearDay()) - 1)
	equation := 229.18 * (0.000075 + 0.001868*math.Cos(gamma) - 0.032077*math.Sin(gamma) -
		0.014615*math.Cos(2*gamma) - 0.040849*math.Sin(2*gamma))
	declination := 0.006918 - 0.399912*math.Cos(gamma) + 0.070257*math.Sin(gamma) -
		0.006758*math.Cos(2*gamma) + 0.000907*math.Sin(2*gamma) -
		0.002697*math.Cos(3*gamma) + 0.00148*math.Sin(3*gamma)
	cosine := (math.Cos(90.833*radians) / (math.Cos(latitude*radians) * math.Cos(declination))) -
		math.Tan(latitude*radians)*math.Tan(declination)
	if cosine < -1 || cosine > 1 || !finite(cosine) {
		return time.Time{}, false
	}
	angle := math.Acos(cosine) / radians
	minutes := 720 - 4*longitude - equation
	if sunrise {
		minutes -= 4 * angle
	} else {
		minutes += 4 * angle
	}
	utc := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	event := utc.Add(time.Duration(minutes * float64(time.Minute))).In(date.Location())
	// UTC dates can differ from the requested local date near the date line.
	if event.Day() != date.Day() {
		if event.Before(time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())) {
			event = event.Add(24 * time.Hour)
		} else {
			event = event.Add(-24 * time.Hour)
		}
	}
	return event, true
}

package databin

// JavaDoubleString renders f as java.lang.Double.toString does.
func JavaDoubleString(f float64) string { return javaDoubleToString(f) }

package langs

func IfElse[T any](cond bool, v1, v2 T) T {
	if cond {
		return v1
	}
	return v2
}

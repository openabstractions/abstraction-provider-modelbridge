package bridge

func moveDirectoryNoReplace(from, to string) error {
	return moveNoReplace(from, to)
}

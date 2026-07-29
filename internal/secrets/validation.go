package secrets

// ValidateKey checks that a key name follows the naming rules:
//   - Contains only alphanumeric characters and underscores
//   - Does not start with a digit
//   - Is at most MaxKeyLength (255) characters long
//   - Is not empty
func ValidateKey(key string) error {
	// TODO: implement in task group 4
	return nil
}

// ValidateValue checks that a raw value does not exceed MaxValueSize (262144 bytes).
func ValidateValue(value string) error {
	// TODO: implement in task group 4
	return nil
}

// ValidateEntries checks the entries array:
//   - Must not be empty
//   - Must not contain duplicate keys (case-insensitive)
//   - Each key and value must pass individual validation
func ValidateEntries(entries []EntryInput) error {
	// TODO: implement in task group 4
	return nil
}

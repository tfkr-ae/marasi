package extensions

import (
	"reflect"
	"testing"
)

func TestStringsLibrary(t *testing.T) {
	tests := []struct {
		name    string
		luaCode string
		want    any
	}{
		{
			name:    "strings:upper should make all characters upper case",
			luaCode: `return marasi.strings:upper("marasi")`,
			want:    "MARASI",
		},
		{
			name:    "strings:lower should make all characters lower case",
			luaCode: `return marasi.strings:lower("MaraSi")`,
			want:    "marasi",
		},
		{
			name:    "strings:reverse should reverse the input string",
			luaCode: `return marasi.strings:reverse("isaram")`,
			want:    "marasi",
		},
		{
			name:    "strings:len should return the correct string length",
			luaCode: `return marasi.strings:len("marasi")`,
			want:    6.0,
		},
		{
			name:    `strings:replace (without replacement and occurence) should fall back to ""`,
			luaCode: `return marasi.strings:replace("marasi test script", " test")`,
			want:    "marasi script",
		},
		{
			name:    `strings:replace (with replacement and without occurence) should fall back to unlimited occurences`,
			luaCode: `return marasi.strings:replace("marasi marasi marasi script", "marasi ", "")`,
			want:    "script",
		},
		{
			name:    `strings:replace (with replacement and with occurence) should replace string n-number of times`,
			luaCode: `return marasi.strings:replace("marasi marasi marasi script", "marasi ", "", 2)`,
			want:    "marasi script",
		},
		{
			name:    "strings:contains should return true if input contains substring",
			luaCode: `return marasi.strings:contains("Authorization: Bearer 12345", "Authorization")`,
			want:    true,
		},
		{
			name:    "strings:contains should return false if input doesn't contain substring",
			luaCode: `return marasi.strings:contains("Authorization: Bearer 12345", "JWT")`,
			want:    false,
		},
		{
			name:    "strings:has_prefix should return true if string has prefix",
			luaCode: `return marasi.strings:has_prefix("Authorization: Bearer 12345", "Authorization:")`,
			want:    true,
		},
		{
			name:    "strings:has_prefix should return false if string doesn't have the prefix",
			luaCode: `return marasi.strings:has_prefix("Authorization: Bearer 12345", "Authorization: Basic")`,
			want:    false,
		},
		{
			name:    "strings:has_suffix should return true if the string has a suffix",
			luaCode: `return marasi.strings:has_suffix("https://marasi.app/document.txt", ".txt")`,
			want:    true,
		},
		{
			name:    "strings:has_suffix should return false if the string doesn't have a suffix",
			luaCode: `return marasi.strings:has_suffix("https://marasi.app/document.txt", ".jpg")`,
			want:    false,
		},
		{
			name:    "strings:split should split string at the separator",
			luaCode: `return marasi.strings:split("marasi, app, split, comma", ", ")`,
			want:    []any{"marasi", "app", "split", "comma"},
		},
		{
			name:    "strings:trim should trim the input string from spaces",
			luaCode: `return marasi.strings:trim(" marasi app   ")`,
			want:    "marasi app",
		},
		{
			name:    "strings:substring should return the substring of a string",
			luaCode: `return marasi.strings:substring("marasi", 0, 3)`,
			want:    "mar",
		},
		{
			name:    "strings:substring should return the substring of a string with multibyte characters",
			luaCode: `return marasi.strings:substring("السلام عليكم", 0, 3)`,
			want:    "الس",
		},
		{
			name:    "strings:substring should correctly clamp start to 0 if input is negative",
			luaCode: `return marasi.strings:substring("marasi", -5, 3)`,
			want:    "mar",
		},
		{
			name:    "strings:substring should correctly clamp end to len(input) if end > len(input)",
			luaCode: `return marasi.strings:substring("marasi", 3, 7)`,
			want:    "asi",
		},
		{
			name:    "strings:substring should correctly clamp end to len(input) if end is not provided",
			luaCode: `return marasi.strings:substring("marasi", 3)`,
			want:    "asi",
		},
		{
			name:    "strings:substring should return an empty string if start > len(inputs)",
			luaCode: `return marasi.strings:substring("marasi", 7, 10)`,
			want:    "",
		},
		{
			name:    "strings:substring should return an empty string if end < start",
			luaCode: `return marasi.strings:substring("marasi", 4, 2)`,
			want:    "",
		},
		{
			name:    "strings:substring should return an empty string if input string is empty",
			luaCode: `return marasi.strings:substring("", 0, 1)`,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := goValue(extension.LuaState, -1)

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("\nwanted:\n%v (%T)\ngot:\n%v (%T)", tt.want, tt.want, got, got)
			}
		})
	}
}

package extensions

import (
	"strings"
	"testing"
)

func TestRandomLibrary(t *testing.T) {
	tests := []struct {
		name          string
		luaCode       string
		validatorFunc func(t *testing.T, got any)
	}{
		{
			name:    "random:int should return a number within the range",
			luaCode: `return marasi.random:int(10, 20)`,
			validatorFunc: func(t *testing.T, got any) {
				num, ok := got.(float64)
				if !ok {
					t.Fatalf("\nwanted:\nfloat64\ngot:\n%T", got)
				}
				if num < 10 || num > 20 {
					t.Errorf("\nwanted:\nnumber between 10 and 20\ngot:\n%v", got)
				}
			},
		},
		{
			name: "random:int should return an err if min > max",
			luaCode: `
				local ok, res = pcall(marasi.random.int, marasi.random, 23, 20)
				if ok then
					return "expected error but got success"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errString, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errString, "minimum value cannot be greater than max") {
					t.Errorf("\nwanted:\nerror message: %s\ngot:\n%s", "minimum value cannot be greater than max", errString)
				}
			},
		},
		{
			name:    "random:string should return a string with the correct length",
			luaCode: `return marasi.random:string(16)`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)

				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}

				if len(str) != 16 {
					t.Errorf("\nwanted:\nlength to be 16\ngot:\n%v", len(str))
				}
			},
		},
		{
			name:    "random:string should use the provided charset",
			luaCode: `return marasi.random:string(16, "A")`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)

				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				if len(str) != 16 {
					t.Errorf("\nwanted:\nlength 16\ngot:\n%d", len(str))
				}

				want := strings.Repeat("A", 16)
				if str != want {
					t.Errorf("\nwanted:\n%q\ngot:\n%q", want, str)
				}
			},
		},
		{
			name:    "random:string should return an empty string for length <= 0",
			luaCode: `return marasi.random:string(0)`,
			validatorFunc: func(t *testing.T, got any) {
				str, ok := got.(string)

				if !ok {
					t.Fatalf("\nwanted:\nstring\ngot:\n%T", got)
				}

				if str != "" {
					t.Errorf("\nwanted:\nempty string\ngot:\n%q", str)
				}
			},
		},
		{
			name: "random:string should error on empty charset",
			luaCode: `
				local ok, res = pcall(marasi.random.string, marasi.random, 10, "")
				if ok then
					return "expected error but got success"
				end
				return res
			`,
			validatorFunc: func(t *testing.T, got any) {
				errString, ok := got.(string)
				if !ok {
					t.Fatalf("\nwanted:\nstring error\ngot:\n%T", got)
				}
				if !strings.Contains(errString, "charset cannot be empty") {
					t.Errorf("\nwanted:\nerror message: %s\ngot:\n%s", "charset cannot be empty", errString)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extension, _ := setupTestExtension(t, "")

			err := extension.ExecuteLua(tt.luaCode)
			if err != nil {
				t.Fatalf("executing lua code %s : %v", tt.luaCode, err)
			}

			got := GoValue(extension.LuaState, -1)

			if tt.validatorFunc != nil {
				tt.validatorFunc(t, got)
			}
		})
	}
}

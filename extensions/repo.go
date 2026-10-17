package extensions

import (
	"fmt"

	"github.com/Shopify/go-lua"
	"github.com/Shopify/goluago/util"
	"github.com/google/uuid"
)

// registerRepoLibrary registers the `marasi.repo` library into the Lua state.
// This library provides access to the traffic repository for querying
// request/response data.
func registerRepoLibrary(extension *Runtime, proxy ProxyService) {
	l := extension.LuaState
	l.Global("marasi")
	if l.IsNil(-1) {
		l.Pop(1)
		return
	}

	newLibrary(extension, "marasi.repo", trafficLibrary(proxy))
	l.SetField(-2, "repo")
	l.Pop(1)
}

// trafficLibrary returns the list of Lua functions for the traffic repository.
func trafficLibrary(proxy ProxyService) []APIFunction {
	return []APIFunction{
		{Name: "get_summary", Signature: "() -> table", Summary: "Retrieves a summary of all request/response pairs in the repository.", Function: func(l *lua.State) int {
			repo, err := proxy.GetTrafficRepo()
			if err != nil {
				lua.Errorf(l, "getting traffic repo: %s", err.Error())
				return 0
			}

			summaries, err := repo.GetRequestResponseSummary()
			if err != nil {
				lua.Errorf(l, "getting summaries: %s", err.Error())
				return 0
			}

			result := make([]map[string]any, len(summaries))
			for i, summary := range summaries {
				result[i] = map[string]any{
					"id":           summary.ID.String(),
					"scheme":       summary.Scheme,
					"method":       summary.Method,
					"host":         summary.Host,
					"path":         summary.Path,
					"status":       summary.Status,
					"status_code":  summary.StatusCode,
					"content_type": summary.ContentType,
					"length":       summary.Length,
					"metadata":     summary.Metadata,
					"requested_at": summary.RequestedAt.UnixMilli(),
					"responded_at": summary.RespondedAt.UnixMilli(),
				}
			}

			util.DeepPush(l, result)
			return 1
		}},
		{Name: "get_details", Signature: "(id: string) -> table", Summary: "Retrieves full details for a specific request/response pair.", Function: func(l *lua.State) int {
			repo, err := proxy.GetTrafficRepo()
			if err != nil {
				lua.Errorf(l, "getting traffic repo: %s", err.Error())
				return 0
			}

			idString := lua.CheckString(l, 2)
			id, err := uuid.Parse(idString)
			if err != nil {
				lua.ArgumentError(l, 2, "invalid UUID")
				return 0
			}

			row, err := repo.GetRequestResponseRow(id)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("getting row details: %s", err.Error()))
				return 0
			}
			result := map[string]any{
				"request": map[string]any{
					"id":           row.Request.ID.String(),
					"scheme":       row.Request.Scheme,
					"method":       row.Request.Method,
					"host":         row.Request.Host,
					"path":         row.Request.Path,
					"raw":          string(row.Request.Raw),
					"metadata":     row.Metadata,
					"requested_at": row.Request.RequestedAt.UnixMilli(),
				},
				"response": map[string]any{
					"id":           row.Response.ID.String(),
					"status":       row.Response.Status,
					"status_code":  row.Response.StatusCode,
					"content_type": row.Response.ContentType,
					"length":       row.Response.Length,
					"raw":          string(row.Response.Raw),
					"metadata":     row.Response.Metadata,
					"responded_at": row.Response.RespondedAt.UnixMilli(),
				},
				"metadata": row.Metadata,
				"note":     row.Note,
				// Prettified copies are not metadata (ADR-0036). A copy is
				// "" when the proxy could not prettify that body.
				"prettified": map[string]any{
					"request":  row.Prettified.Request,
					"response": row.Prettified.Response,
				},
			}
			util.DeepPush(l, result)
			return 1
		}},
		{Name: "get_metadata", Signature: "(id: string) -> table", Summary: "Retrieves the metadata associated with a specific request/response pair.", Function: func(l *lua.State) int {
			repo, err := proxy.GetTrafficRepo()
			if err != nil {
				lua.Errorf(l, "getting traffic repo: %s", err.Error())
				return 0
			}

			idString := lua.CheckString(l, 2)
			id, err := uuid.Parse(idString)
			if err != nil {
				lua.ArgumentError(l, 2, "invalid UUID")
				return 0
			}

			metadata, err := repo.GetMetadata(id)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("getting metadata for %s : %s", idString, err.Error()))
				return 0
			}
			util.DeepPush(l, metadata)
			return 1
		}},
		{Name: "set_metadata", Signature: "(id: string, metadata: table)", Summary: "Replaces the metadata for a specific request/response pair.", Function: func(l *lua.State) int {
			repo, err := proxy.GetTrafficRepo()
			if err != nil {
				lua.Errorf(l, "getting traffic repo: %s", err.Error())
				return 0
			}

			idString := lua.CheckString(l, 2)
			id, err := uuid.Parse(idString)
			if err != nil {
				lua.ArgumentError(l, 2, "invalid UUID")
				return 0
			}

			if l.TypeOf(3) != lua.TypeTable {
				lua.ArgumentError(l, 3, "metadata must be a key-value table")
				return 0
			}

			val := ParseTable(l, 3, GoValue)

			metadata, ok := val.(map[string]any)
			if !ok {
				lua.ArgumentError(l, 3, "metadata must be a key-value table, not an array")
				return 0
			}

			err = repo.UpdateMetadata(metadata, id)
			if err != nil {
				lua.Errorf(l, "updating metadata for %s : %s", idString, err.Error())
				return 0
			}
			return 0
		}},
		{Name: "get_note", Signature: "(id: string) -> string", Summary: "Retrieves the note associated with a specific request/response pair.", Function: func(l *lua.State) int {
			repo, err := proxy.GetTrafficRepo()
			if err != nil {
				lua.Errorf(l, "getting traffic repo: %s", err.Error())
				return 0
			}

			idString := lua.CheckString(l, 2)
			id, err := uuid.Parse(idString)
			if err != nil {
				lua.ArgumentError(l, 2, "invalid UUID")
				return 0
			}

			note, err := repo.GetNote(id)
			if err != nil {
				lua.Errorf(l, fmt.Sprintf("getting note for %s : %s", idString, err.Error()))
				return 0
			}
			util.DeepPush(l, note)
			return 1
		}},
		{Name: "set_note", Signature: "(id: string, note: string)", Summary: "Updates the note for a specific request/response pair.", Function: func(l *lua.State) int {
			repo, err := proxy.GetTrafficRepo()
			if err != nil {
				lua.Errorf(l, "getting traffic repo: %s", err.Error())
				return 0
			}

			idString := lua.CheckString(l, 2)
			id, err := uuid.Parse(idString)
			if err != nil {
				lua.ArgumentError(l, 2, "invalid UUID")
				return 0
			}

			note := lua.CheckString(l, 3)

			err = repo.UpdateNote(id, note)
			if err != nil {
				lua.Errorf(l, "updating note for %s : %s", idString, err.Error())
				return 0
			}
			return 0
		}},
		{Name: "search_by_metadata", Signature: "(path: string, value: any) -> table", Summary: "Retrieves requests where the value at the specified JSON path matches the provided value. path is a JSON path such as \"$.intercepted\".", Function: func(l *lua.State) int {
			repo, err := proxy.GetTrafficRepo()
			if err != nil {
				lua.Errorf(l, "getting traffic repo: %s", err.Error())
				return 0
			}

			path := lua.CheckString(l, 2)
			value := GoValue(l, 3)

			summaries, err := repo.SearchByMetadata(path, value)
			if err != nil {
				lua.Errorf(l, "searching by metadata: %s", err.Error())
				return 0
			}

			result := make([]map[string]any, len(summaries))
			for i, summary := range summaries {
				result[i] = map[string]any{
					"id":           summary.ID.String(),
					"scheme":       summary.Scheme,
					"method":       summary.Method,
					"host":         summary.Host,
					"path":         summary.Path,
					"status":       summary.Status,
					"status_code":  summary.StatusCode,
					"content_type": summary.ContentType,
					"length":       summary.Length,
					"metadata":     summary.Metadata,
					"requested_at": summary.RequestedAt.UnixMilli(),
					"responded_at": summary.RespondedAt.UnixMilli(),
				}
			}

			util.DeepPush(l, result)
			return 1
		}},
	}
}

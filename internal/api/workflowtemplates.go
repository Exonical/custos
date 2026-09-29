package api

import (
	"net/http"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/workflowtemplates"
)

// Built-in workflow templates are platform content, readable by any
// authenticated principal; instantiating one is an ordinary
// createWorkflow + createWorkflowVersion under the caller's rights.

func templateSummaryDTO(t workflowtemplates.Template) map[string]any {
	return map[string]any{
		"id":          t.ID,
		"title":       t.Title,
		"summary":     t.Summary,
		"tags":        t.Tags,
		"description": t.Description,
	}
}

func listWorkflowTemplates(w http.ResponseWriter, r *http.Request) {
	all, err := workflowtemplates.All()
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	items := make([]any, 0, len(all))
	for _, t := range all {
		items = append(items, templateSummaryDTO(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func getWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, ok, err := workflowtemplates.Get(r.PathValue("template"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	if !ok {
		httpx.WriteError(ctx, w, apperr.New(apperr.NotFound, "NOT_FOUND",
			"workflow template not found"))
		return
	}
	out := templateSummaryDTO(t)
	out["spec"] = t.Spec
	out["yaml"] = t.YAML
	httpx.WriteJSON(w, http.StatusOK, out)
}

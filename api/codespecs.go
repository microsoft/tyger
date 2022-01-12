package api

import (
	"errors"
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/database"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/jsonbody"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/model"
)

func (api *Api) UpsertCodespec(w http.ResponseWriter, r *http.Request, name string) {
	codespec := model.Codespec{}
	if err := jsonbody.DecodeJSONBody(w, r, &codespec); err != nil {
		var mbe *jsonbody.MalformedBodyError
		if errors.As(err, &mbe) {
			writeError(w, mbe.StatusCode, "InvalidInput", mbe.Message)
			return
		}

		writeInternalServerError(w, r, err)
		return
	}

	version, err := api.repository.UpsertCodespec(r.Context(), name, codespec)
	if err != nil {
		writeInternalServerError(w, r, err)
		return
	}
	w.Header().Add("Location", fmt.Sprintf("v1/codespecs/%s/versions/%d", name, version))
	statusCode := http.StatusOK
	if version == 1 {
		statusCode = http.StatusCreated
	}
	writeJson(w, statusCode, codespec)
}

func (api *Api) GetLatestCodespec(w http.ResponseWriter, r *http.Request, name string) {
	codespec, version, err := api.repository.GetLatestCodespec(r.Context(), name)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			writeNotFound(w)
			return
		}

		writeInternalServerError(w, r, err)
		return
	}

	w.Header().Add("Location", fmt.Sprintf("v1/codespecs/%s/versions/%d", name, *version))
	writeJson(w, http.StatusOK, codespec)
}

func (api *Api) GetCodespecVersion(w http.ResponseWriter, r *http.Request, name string, version int) {
	codespec, err := api.repository.GetCodespecVersion(r.Context(), name, version)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			writeNotFound(w)
			return
		}

		writeInternalServerError(w, r, err)
		return
	}

	writeJson(w, http.StatusOK, codespec)
}

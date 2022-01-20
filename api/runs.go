package api

import (
	"errors"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/jsonbody"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/model"
)

func (api *Api) CreateRun(w http.ResponseWriter, r *http.Request) {
	run := model.Run{}
	if err := jsonbody.DecodeJSONBody(w, r, &run); err != nil {
		var mbe *jsonbody.MalformedBodyError
		if errors.As(err, &mbe) {
			writeError(w, mbe.StatusCode, "InvalidInput", mbe.Message)
			return
		}

		writeInternalServerError(w, r, err)
		return
	}

	responseRun, err := api.k8sManager.CreateRun(r.Context(), run)
	if err != nil {
		var validationError *model.ValidationError
		if errors.As(err, &validationError) {
			writeError(w, http.StatusBadRequest, "InvalidInput", validationError.Message)
			return
		}
		writeInternalServerError(w, r, err)
		return
	}

	writeJson(w, http.StatusCreated, responseRun)
}

func (api *Api) GetRun(w http.ResponseWriter, r *http.Request, id string) {
	run, err := api.k8sManager.GetRun(r.Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			writeNotFound(w)
			return
		}
		writeInternalServerError(w, r, err)
		return
	}

	writeJson(w, http.StatusOK, run)
}

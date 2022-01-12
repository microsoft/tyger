package api

import (
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/model"
	"github.com/rs/zerolog/log"
)

func (api *Api) CreateBuffer(w http.ResponseWriter, r *http.Request) {
	id, err := api.bufferManager.CreateBuffer(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("Unable to create container")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Add("Location", fmt.Sprintf("%s/v1/buffers/%s", api.baseUri, id))
	writeJson(w, http.StatusCreated, Buffer{Id: id})
}

func (api *Api) GetBufferByID(w http.ResponseWriter, r *http.Request, id string) {
	err := api.bufferManager.GetBuffer(r.Context(), id)

	if err != nil {
		if err == model.ErrNotFound {
			writeNotFound(w)
			return
		}

		log.Err(err).Send()
		writeInternalServerError(w, err)
		return
	}

	writeJson(w, http.StatusOK, Buffer{Id: id})
}

func (api *Api) GetBufferAccessUri(w http.ResponseWriter, r *http.Request, id string, params GetBufferAccessUriParams) {
	uri, err := api.bufferManager.GetSasUri(r.Context(), id, params.Writeable != nil && *params.Writeable, true /* externalAccess */)

	if err != nil {
		if err == model.ErrNotFound {
			writeNotFound(w)
			return
		}

		log.Err(err).Send()
		return
	}

	writeJson(w, http.StatusCreated, BufferAccess{Uri: uri})
}

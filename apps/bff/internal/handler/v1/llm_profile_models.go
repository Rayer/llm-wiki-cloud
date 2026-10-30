package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
)

const maxLLMProfileRequestBytes = 32 << 10

func decodeLLMProfileJSON(c *gin.Context, input *llmProfileInput) error {
	return decodeStrictLLMJSON(c, maxLLMProfileRequestBytes, input)
}

func decodeStrictLLMJSON(c *gin.Context, maxBytes int64, target any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func profileInputModels(input llmProfileInput, current *llmProfileDocument) (llmProfileModels, error) {
	legacyModel := strings.TrimSpace(input.Model)
	if len(input.Models) == 0 {
		if legacyModel == "" || len(legacyModel) > 200 {
			return llmProfileModels{}, errLLMProfileInvalid
		}
		if current != nil {
			currentModels, ok := llmProfileRoleModels(*current)
			if !ok {
				return llmProfileModels{}, errLLMProfileInvalid
			}
			if currentModels.Fast != currentModels.Heavy {
				return llmProfileModels{}, errLLMProfileRoleModelsRequired
			}
		}
		return llmProfileModels{Fast: legacyModel, Heavy: legacyModel}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(input.Models))
	decoder.DisallowUnknownFields()
	var models llmProfileModels
	if err := decoder.Decode(&models); err != nil || decoder.Decode(new(any)) != io.EOF {
		return llmProfileModels{}, errLLMProfileInvalid
	}
	models.Fast, models.Heavy = strings.TrimSpace(models.Fast), strings.TrimSpace(models.Heavy)
	if models.Fast == "" || models.Heavy == "" || len(models.Fast) > 200 || len(models.Heavy) > 200 ||
		(legacyModel != "" && legacyModel != models.Heavy) {
		return llmProfileModels{}, errLLMProfileInvalid
	}
	return models, nil
}

func llmProfileRoleModels(profile llmProfileDocument) (llmProfileModels, bool) {
	models := profile.Models
	models.Fast, models.Heavy = strings.TrimSpace(models.Fast), strings.TrimSpace(models.Heavy)
	if models.Fast == "" && models.Heavy == "" {
		legacy := strings.TrimSpace(profile.Model)
		if legacy == "" || len(legacy) > 200 {
			return llmProfileModels{}, false
		}
		return llmProfileModels{Fast: legacy, Heavy: legacy}, true
	}
	if models.Fast == "" || models.Heavy == "" || len(models.Fast) > 200 || len(models.Heavy) > 200 ||
		(profile.Model != "" && strings.TrimSpace(profile.Model) != models.Heavy) {
		return llmProfileModels{}, false
	}
	return models, true
}

func profileRevisionRef(profile *firestore.DocumentRef, revision int64) *firestore.DocumentRef {
	return profile.Collection(profileRevisionCollection).Doc(strconv.FormatInt(revision, 10))
}

package engine

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/validate"
)

func TestFreezeImagePullSecretRefsStoresOnlyReferenceIDs(t *testing.T) {
	usernameID := uuid.New()
	passwordID := uuid.New()
	uses := map[string]workflowspec.SecretUse{
		"registry-user":  {Ref: "registry-user", Use: "image_pull"},
		"registry-token": {Ref: "registry-token", Use: "image_pull"},
	}
	infos := map[string]validate.SecretReferenceInfo{
		"registry-user": {
			ID: usernameID.String(), Kind: "generic", AllowedUses: []string{"image_pull"},
		},
		"registry-token": {
			ID: passwordID.String(), Kind: "generic", AllowedUses: []string{"image_pull"},
		},
	}
	refs, denial := freezeImagePullSecretRefs(&workflowspec.ImagePullSecret{
		UsernameSecret: "registry-user", PasswordSecret: "registry-token",
	}, uses, infos)
	if denial != nil {
		t.Fatalf("freeze image pull refs: %v", denial)
	}
	if len(refs) != 2 || refs[0].Name != "CUSTOS_IMAGE_PULL_USERNAME" ||
		refs[0].ReferenceID != usernameID || refs[0].Handle != "registry-user" ||
		refs[0].Mode != "image_pull" || refs[1].Name != "CUSTOS_IMAGE_PULL_PASSWORD" ||
		refs[1].ReferenceID != passwordID || refs[1].Handle != "registry-token" ||
		refs[1].Mode != "image_pull" {
		t.Fatalf("unexpected frozen image pull refs: %+v", refs)
	}
}

func TestFreezeImagePullSecretRefsKeepsLiteralUsernameOutOfSecretRefs(t *testing.T) {
	refs, denial := freezeImagePullSecretRefs(&workflowspec.ImagePullSecret{
		Username: "robot$ci", PasswordSecret: "registry-token",
	}, map[string]workflowspec.SecretUse{
		"registry-token": {Ref: "registry-token", Use: "image_pull"},
	}, map[string]validate.SecretReferenceInfo{
		"registry-token": {
			ID: uuid.New().String(), Kind: "generic", AllowedUses: []string{"image_pull"},
		},
	})
	if denial != nil {
		t.Fatalf("freeze image pull refs: %v", denial)
	}
	if len(refs) != 1 || refs[0].Name != "CUSTOS_IMAGE_PULL_PASSWORD" {
		t.Fatalf("literal username was recorded as a secret ref: %+v", refs)
	}
}

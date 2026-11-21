package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sync"
)

//go:embed assets/bot-avatars.js
var botAvatarJS string

//go:embed assets/bot-avatars/catalog.json assets/bot-avatars/*/*.webp
var botAvatarFiles embed.FS

type avatarPack struct {
	English string `json:"en"`
	Chinese string `json:"zh"`
	Avatars []struct {
		File    string `json:"file"`
		English string `json:"en"`
		Chinese string `json:"zh"`
		Slot    string `json:"slot,omitempty"`
		Source  string `json:"src,omitempty"`
	} `json:"avatars"`
}

func loadBotAvatarPacks() ([]avatarPack, error) {
	const root = "assets/bot-avatars/"
	catalog, err := botAvatarFiles.ReadFile(root + "catalog.json")
	if err != nil {
		return nil, err
	}
	var packs []avatarPack
	if err := json.Unmarshal(catalog, &packs); err != nil {
		return nil, err
	}
	for _, pack := range packs {
		for i := range pack.Avatars {
			avatar := &pack.Avatars[i]
			picture, err := botAvatarFiles.ReadFile(root + avatar.File)
			if err != nil {
				return nil, fmt.Errorf("avatar %s: %w", avatar.File, err)
			}
			avatar.Source = "data:image/webp;base64," + base64.StdEncoding.EncodeToString(picture)
		}
	}
	return packs, nil
}

var botAvatarScript = sync.OnceValue(func() string {
	packs, err := loadBotAvatarPacks()
	if err != nil {
		log.Printf("Load bot avatars: %v", err)
		return ""
	}
	payload, _ := json.Marshal(packs)
	return botAvatarJS + "(\n" + string(payload) + "\n);\n"
})

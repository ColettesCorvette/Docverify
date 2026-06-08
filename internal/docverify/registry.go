package docverify

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type nodeInfo struct {
	IP         string
	LastSeenAt time.Time
}

var (
	nodesMutex sync.Mutex
	activeNodes = make(map[string]*nodeInfo)
)

// StartRegistry démarre le serveur HTTP d'enregistrement de flotte sur le port indiqué.
func StartRegistry(port string) {
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}

	templatePath := "deploy/fleet/Caddyfile.template"
	outputPath := "deploy/fleet/Caddyfile"

	// Vérification de base des chemins
	if _, err := os.Stat(templatePath); err != nil {
		log.Printf("[registry] Avertissement : template Caddyfile introuvable à %s. La régénération échouera.", templatePath)
	}

	// Routine de nettoyage des nœuds inactifs (ex: crash ou arrêt brutal)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			nodesMutex.Lock()
			changed := false
			now := time.Now()
			for ip, node := range activeNodes {
				if now.Sub(node.LastSeenAt) > 90*time.Second {
					log.Printf("[registry] Nœud inactif depuis >90s éjecté de la flotte : %s", ip)
					delete(activeNodes, ip)
					changed = true
				}
			}
			if changed {
				if err := regenerateCaddyfile(templatePath, outputPath); err != nil {
					log.Printf("[registry] Échec de la mise à jour Caddyfile : %v", err)
				}
			}
			nodesMutex.Unlock()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", handleRegister(templatePath, outputPath))

	log.Printf("[registry] Serveur d'orchestration dynamique sur %s", port)
	log.Fatal(http.ListenAndServe(port, mux))
}

func handleRegister(templatePath, outputPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := r.FormValue("ip")
		if ip == "" {
			http.Error(w, "Paramètre 'ip' manquant", http.StatusBadRequest)
			return
		}
		ip = strings.TrimSpace(ip)

		nodesMutex.Lock()
		defer nodesMutex.Unlock()

		node, exists := activeNodes[ip]
		if !exists {
			log.Printf("[registry] Nouveau nœud s'invite dans la flotte : %s", ip)
			activeNodes[ip] = &nodeInfo{
				IP:         ip,
				LastSeenAt: time.Now(),
			}
			if err := regenerateCaddyfile(templatePath, outputPath); err != nil {
				log.Printf("[registry] Échec de la mise à jour Caddyfile : %v", err)
				http.Error(w, "Erreur régénération Caddyfile", http.StatusInternalServerError)
				return
			}
		} else {
			node.LastSeenAt = time.Now()
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
}

func regenerateCaddyfile(templatePath, outputPath string) error {
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("lecture template %s : %w", templatePath, err)
	}

	var upstreams []string
	for ip := range activeNodes {
		upstreams = append(upstreams, fmt.Sprintf("to %s:8680", ip))
	}

	upstreamBlock := strings.Join(upstreams, "\n\t\t")
	if len(upstreams) == 0 {
		upstreamBlock = "# Aucun nœud actif"
	}

	content := strings.Replace(string(data), "# {{UPSTREAMS}}", upstreamBlock, 1)

	// S'assurer que le dossier parent existe
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("creation dossier parent : %w", err)
	}

	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("ecriture Caddyfile %s : %w", outputPath, err)
	}

	log.Printf("[registry] Caddyfile actualisé avec %d nœud(s). Rechargement de Caddy...", len(activeNodes))

	// Exécuter la commande caddy reload
	cmd := exec.Command("caddy", "reload", "--config", outputPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Log l'erreur mais ne bloque pas (Caddy n'est peut-être pas installé sur l'hôte qui lance docverify)
		log.Printf("[registry] Avertissement : impossible de recharger Caddy (%v). Output: %s", err, string(out))
	} else {
		log.Printf("[registry] Caddy rechargé avec succès à chaud")
	}

	return nil
}

package docverify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// callbackClient sert aux POST de webhook (mode asynchrone). Timeout court (un
// callback qui traîne ne doit pas retenir une goroutine), pas de suivi des
// redirections (anti-SSRF par rebond), et un dialer qui refuse les IP
// dangereuses (voir safeCallbackDial).
var callbackClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse // ne pas suivre les redirections
	},
	Transport: &http.Transport{DialContext: safeCallbackDial},
}

// newRequestID génère un identifiant unique (UUID v4) par requête. Il sert de
// job_id pour les requêtes asynchrones et de X-Request-Id pour la traçabilité
// en mode synchrone. Implémenté avec crypto/rand pour garder go.mod sans
// dépendance externe.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand n'échoue qu'en cas de défaillance système : fallback horodaté.
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// validCallbackURL valide une URL de callback : schéma http/https, hôte présent,
// et — si DOCVERIFY_CALLBACK_ALLOWED_HOSTS est défini — hôte figurant dans cette
// liste blanche. C'est une validation syntaxique ; le filtrage des IP
// dangereuses (anti-SSRF) a lieu au moment du dial dans safeCallbackDial.
//
// Anti-SSRF : sans liste blanche, seules les IP loopback/link-local/etc. sont
// refusées (au dial) ; un callback vers un autre service du réseau interne
// reste possible. Pour fermer complètement le SSRF interne, définir la liste
// blanche sur le ou les hôtes de Regia.
func validCallbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	if allow := callbackAllowlist(); len(allow) > 0 && !allow[strings.ToLower(u.Hostname())] {
		return false
	}
	return true
}

// callbackAllowlist lit la liste blanche d'hôtes autorisés pour les callbacks
// depuis DOCVERIFY_CALLBACK_ALLOWED_HOSTS (séparés par des virgules). Vide = pas
// de restriction par hôte.
func callbackAllowlist() map[string]bool {
	raw := os.Getenv("DOCVERIFY_CALLBACK_ALLOWED_HOSTS")
	if raw == "" {
		return nil
	}
	m := map[string]bool{}
	for _, h := range strings.Split(raw, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			m[h] = true
		}
	}
	return m
}

// safeCallbackDial résout l'hôte du callback et refuse de se connecter à une IP
// loopback, link-local (dont l'endpoint metadata 169.254.169.254), non spécifiée
// ou multicast — cibles classiques d'un SSRF. Les IP privées RFC1918 restent
// autorisées : le callback légitime de Regia est sur le réseau interne (utiliser
// la liste blanche d'hôtes pour le restreindre). La connexion vise directement
// l'IP validée, sans fenêtre TOCTOU / DNS-rebinding.
func safeCallbackDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	var lastErr error
	for _, ip := range ips {
		if isBlockedCallbackIP(ip.IP) {
			lastErr = fmt.Errorf("callback refusé vers une IP interdite (%s)", ip.IP)
			continue
		}
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("aucune IP autorisée pour %q", host)
	}
	return nil, lastErr
}

// isBlockedCallbackIP retourne true pour les classes d'IP qui ne sont jamais une
// cible de callback légitime et servent au SSRF.
func isBlockedCallbackIP(ip net.IP) bool {
	return ip == nil ||
		ip.IsLoopback() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}

// asyncCallback est le payload POSTé au callback_url du client en fin de
// traitement asynchrone.
type asyncCallback struct {
	JobID  string          `json:"job_id"`
	Status string          `json:"status"` // "done" | "error"
	Result *AnalysisResult `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// processAsync exécute le job hors de la connexion HTTP initiale (le client a
// déjà reçu son 202 + job_id), puis POST le verdict au callback_url. Le contexte
// est indépendant de la requête : la fin du handler ne doit pas annuler le
// traitement. Le job passe par la même queue que le mode synchrone, donc il
// respecte la limite de workers.
func processAsync(q *Queue, job Job, jobID, callbackURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	res := q.Submit(ctx, job)

	cb := asyncCallback{JobID: jobID}
	if res.Err != nil {
		cb.Status = "error"
		cb.Error = res.Err.Error()
	} else {
		cb.Status = "done"
		res.Analysis.RawText = "" // RGPD : pas de texte OCR dans le callback
		cb.Result = &res.Analysis
	}

	postCallback(callbackURL, cb)
}

// postCallback POST le résultat au callback_url, avec un retry sur échec. Un
// callback en échec est seulement loggé : on ne peut rien renvoyer au client
// (qui a déjà son 202), c'est à Regia de prévoir une relance/réconciliation.
func postCallback(callbackURL string, cb asyncCallback) {
	body, err := json.Marshal(cb)
	if err != nil {
		log.Printf("[async %s] marshal callback échoué: %v", cb.JobID, err)
		return
	}

	for attempt := 1; attempt <= 2; attempt++ {
		resp, err := callbackClient.Post(callbackURL, "application/json", bytes.NewReader(body))
		if err == nil && resp.StatusCode < 300 {
			resp.Body.Close()
			log.Printf("[async %s] callback livré (%s → HTTP %d)", cb.JobID, cb.Status, resp.StatusCode)
			return
		}
		
		var errReason string
		if err != nil {
			errReason = err.Error()
		} else if resp != nil {
			errReason = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}

		if resp != nil {
			resp.Body.Close()
		}
		log.Printf("[async %s] callback tentative %d/2 échouée: %s", cb.JobID, attempt, errReason)
		if attempt < 2 {
			time.Sleep(time.Second)
		}
	}
	log.Printf("[async %s] callback abandonné après 2 tentatives vers %s", cb.JobID, callbackURL)
}

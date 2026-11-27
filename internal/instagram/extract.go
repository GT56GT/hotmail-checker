package instagram

import (
	"encoding/json"
	"strconv"
	"strings"
)

// detail is the account information scraped from the edit form and profile.
type detail struct {
	Username    string
	FullName    string
	Email       string
	Phone       string
	Biography   string
	ExternalURL string
	UserID      string
	Followers   string
	Following   string
	Posts       string
	Verified    string
	Business    string
	Category    string
	Private     string
}

func (d detail) asMeta() map[string]string {
	m := map[string]string{}
	put := func(k, v string) {
		v = strings.TrimSpace(v)
		if v != "" && v != "null" {
			m[k] = v
		}
	}
	put("Username", d.Username)
	put("Full name", d.FullName)
	put("User ID", d.UserID)
	put("Followers", d.Followers)
	put("Following", d.Following)
	put("Posts", d.Posts)
	put("Verified", d.Verified)
	put("Business", d.Business)
	put("Category", d.Category)
	put("Private", d.Private)
	put("Email", d.Email)
	put("Phone", d.Phone)
	put("Bio", truncate(d.Biography, 120))
	put("Link", d.ExternalURL)
	return m
}

// webFormData is the shape returned by /api/v1/accounts/edit/web_form_data/.
type webFormData struct {
	Status   string `json:"status"`
	FormData struct {
		Username    string `json:"username"`
		FullName    string `json:"full_name"`
		Email       string `json:"email"`
		PhoneNumber string `json:"phone_number"`
		Biography   string `json:"biography"`
		ExternalURL string `json:"external_url"`
	} `json:"form_data"`
}

// currentUser is the shape returned by /api/v1/accounts/current_user/.
type currentUser struct {
	Status string `json:"status"`
	User   struct {
		PK       int64  `json:"pk"`
		Username string `json:"username"`
		FullName string `json:"full_name"`
		Email    string `json:"email"`
		Phone    string `json:"phone_number"`
		Bio      string `json:"biography"`
		External string `json:"external_url"`
		IsVerif  bool   `json:"is_verified"`
		IsBiz    bool   `json:"is_business"`
		IsPriv   bool   `json:"is_private"`
	} `json:"user"`
}

// profileInfo is the shape returned by /api/v1/users/web_profile_info/.
type profileInfo struct {
	Data struct {
		User struct {
			Username    string `json:"username"`
			FullName    string `json:"full_name"`
			IsVerified  bool   `json:"is_verified"`
			IsBusiness  bool   `json:"is_business_account"`
			IsPrivate   bool   `json:"is_private"`
			Category    string `json:"category_name"`
			FollowerCnt struct {
				Count int64 `json:"count"`
			} `json:"edge_followed_by"`
			FollowCnt struct {
				Count int64 `json:"count"`
			} `json:"edge_follow"`
			MediaCnt struct {
				Count int64 `json:"count"`
			} `json:"edge_owner_to_timeline_media"`
		} `json:"user"`
	} `json:"data"`
}

// parseFormData reads the edit form payload.
func parseFormData(body []byte) (detail, bool) {
	var f webFormData
	if err := json.Unmarshal(body, &f); err != nil {
		return detail{}, false
	}
	if f.FormData.Username == "" && f.FormData.Email == "" {
		return detail{}, false
	}

	d := detail{
		Username:    f.FormData.Username,
		FullName:    f.FormData.FullName,
		Email:       f.FormData.Email,
		Phone:       f.FormData.PhoneNumber,
		Biography:   f.FormData.Biography,
		ExternalURL: f.FormData.ExternalURL,
	}
	return d, true
}

// parseCurrentUser reads the current_user payload.
func parseCurrentUser(body []byte) (detail, bool) {
	var u currentUser
	if err := json.Unmarshal(body, &u); err != nil {
		return detail{}, false
	}
	if u.User.Username == "" {
		return detail{}, false
	}

	d := detail{
		Username:    u.User.Username,
		FullName:    u.User.FullName,
		Email:       u.User.Email,
		Phone:       u.User.Phone,
		Biography:   u.User.Bio,
		ExternalURL: u.User.External,
		UserID:      strconv.FormatInt(u.User.PK, 10),
		Verified:    yesNo(u.User.IsVerif),
		Business:    yesNo(u.User.IsBiz),
		Private:     yesNo(u.User.IsPriv),
	}
	return d, true
}

// mergeProfile adds follower counts and flags from web_profile_info.
func mergeProfile(d *detail, body []byte) {
	var p profileInfo
	if err := json.Unmarshal(body, &p); err != nil {
		return
	}
	u := p.Data.User
	if u.Username == "" {
		return
	}

	if d.Username == "" {
		d.Username = u.Username
	}
	if d.FullName == "" {
		d.FullName = u.FullName
	}
	if u.FollowerCnt.Count > 0 || d.Followers == "" {
		d.Followers = strconv.FormatInt(u.FollowerCnt.Count, 10)
	}
	if u.FollowCnt.Count > 0 || d.Following == "" {
		d.Following = strconv.FormatInt(u.FollowCnt.Count, 10)
	}
	if u.MediaCnt.Count > 0 || d.Posts == "" {
		d.Posts = strconv.FormatInt(u.MediaCnt.Count, 10)
	}
	d.Verified = yesNo(u.IsVerified)
	d.Business = yesNo(u.IsBusiness)
	d.Private = yesNo(u.IsPrivate)
	if u.Category != "" {
		d.Category = u.Category
	}
}

// requiresLogin detects the JSON Instagram returns when a session is not valid.
func requiresLogin(body []byte) bool {
	s := string(body)
	if strings.Contains(s, `"require_login":true`) || strings.Contains(s, `"require_login": true`) {
		return true
	}
	if strings.Contains(s, `"message":"login_required"`) || strings.Contains(s, `"login_required"`) {
		return true
	}
	return false
}

// isCheckpoint detects a challenge page, which means the session is real but
// Instagram wants extra verification.
func isCheckpoint(body []byte) bool {
	s := string(body)
	return strings.Contains(s, "checkpoint_required") ||
		strings.Contains(s, "challenge_required") ||
		strings.Contains(s, "consent_required")
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

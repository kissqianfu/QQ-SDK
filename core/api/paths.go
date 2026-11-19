// 平台 REST 路径的唯一来源：静态路径用常量，带动态段的一律走构造器（内含转义）。
// 路径是平台事实（/v2 前缀、下划线命名、/users/@me 不带版本前缀）。

package api

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/kissqianfu/QQ-SDK/core/event"
)

// 静态路径前缀（不含动态段）。
const (
	groupsPath       = "/v2/groups"
	usersPath        = "/v2/users"
	menuPath         = "/v2/menu"
	panelsPath       = "/v2/panels"
	mediaPath        = "/v2/media-images"
	urlLinkPath      = "/v2/generate_url_link"
	strategyBasePath = groupsPath + "/join_approval_strategy"
	// 素材上传路径与 mediaPath 平级。
	materialPreparePath  = mediaPath + "/prepare"
	materialCompletePath = mediaPath + "/complete"
	// mePath 官方口径不带 /v2 前缀。
	mePath = "/users/@me"
	// interactionsPath 同上，互动事件回应不带 /v2。
	interactionsPath = "/interactions"
)

// itemPath 拼「前缀 + 单个 ID + 后缀」，全包唯一的转义点。
// "." / ".." 是 URL 的路径语义字符，url.PathEscape 不会转义它们，必须显式编码，
// 否则 Target.ID = ".." 能把请求改写到同主机的其他路径。
func itemPath(prefix, id, suffix string) string {
	if id == "." || id == ".." {
		id = strings.ReplaceAll(id, ".", "%2E")
	}
	return prefix + "/" + url.PathEscape(id) + suffix
}

// groupMemberPath 拼 /v2/groups/{group_openid}/members/{member_openid}（两段均转义）。
func groupMemberPath(groupOpenID, memberOpenID string) string {
	return itemPath(groupsPath, groupOpenID, "/members/"+url.PathEscape(memberOpenID))
}

// approvalJoinRequestPath 拼 /v2/groups/{group_openid}/approval_join_request/{member_openid}。
func approvalJoinRequestPath(groupOpenID, memberOpenID string) string {
	return itemPath(groupsPath, groupOpenID, "/approval_join_request/"+url.PathEscape(memberOpenID))
}

// sceneMessagePath 拼「场景路径 + /messages/{msg_id}」（撤回等消息维度资源，msg_id 转义）。
func sceneMessagePath(scene event.Scene, targetID, msgID string) (string, error) {
	return scenePath(scene, targetID, "/messages/"+url.PathEscape(msgID))
}

// groupPath 拼 /v2/groups/{group_openid} + 后缀。
func groupPath(groupOpenID, suffix string) string {
	return itemPath(groupsPath, groupOpenID, suffix)
}

// scenePath 拼场景路径：群聊 /v2/groups/{id}、单聊 /v2/users/{id}，再拼后缀。
func scenePath(scene event.Scene, targetID, suffix string) (string, error) {
	if targetID == "" {
		return "", fmt.Errorf("api: 目标 id 不能为空")
	}
	switch scene {
	case event.SceneGroup:
		return itemPath(groupsPath, targetID, suffix), nil
	case event.SceneC2C:
		return itemPath(usersPath, targetID, suffix), nil
	default:
		return "", fmt.Errorf("api: 未知场景 %d", scene)
	}
}

// messagePath 拼发消息路径（按场景区分群聊/单聊）。
func messagePath(target Target) (string, error) {
	return scenePath(target.Scene, target.ID, "/messages")
}

// userPath 拼 /v2/users/{user_openid} + 后缀（用户维度资源，如 /info）。
func userPath(userOpenID, suffix string) string {
	return itemPath(usersPath, userOpenID, suffix)
}

// panelPath 拼 /v2/panels/{panel_id} + 后缀。
func panelPath(panelID, suffix string) string {
	return itemPath(panelsPath, panelID, suffix)
}

// strategyPath 拼 /v2/groups/join_approval_strategy/{strategy_id} + 后缀。
// 注意：策略挂在 groups 下但不带 {group_openid} 段，与 groupPath 不是一族。
func strategyPath(strategyID, suffix string) string {
	return itemPath(strategyBasePath, strategyID, suffix)
}

// materialPath 拼 /v2/media-images/{image_id} + 后缀。
func materialPath(imageID, suffix string) string {
	return itemPath(mediaPath, imageID, suffix)
}

// interactionPath 拼 /interactions/{interaction_id}。
func interactionPath(interactionID string) string {
	return itemPath(interactionsPath, interactionID, "")
}

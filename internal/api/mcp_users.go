package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"qingzhou/internal/auth"
	"qingzhou/internal/idgen"
	"qingzhou/internal/store"
)

const mcpGiB int64 = 1 << 30

// ---- #139 Input Structs ----

type mcpProvisionUserAccessIn struct {
	UserID       int64  `json:"user_id,omitempty" jsonschema:"已有用户 ID（与 username 二选一）"`
	Username     string `json:"username,omitempty" jsonschema:"用户名（3-32 位字母/数字/下划线；若不存在则自动建号）"`
	Password     string `json:"password,omitempty" jsonschema:"新建用户时的密码（至少 6 位，留空自动随机生成并在本次返回）"`
	Email        string `json:"email,omitempty" jsonschema:"新建用户时的可选邮箱"`
	PackageID    int64  `json:"package_id" jsonschema:"要分配给该用户的套餐 ID（必填）"`
	DurationDays int64  `json:"duration_days,omitempty" jsonschema:"自定义天数（0 表示使用套餐默认时长）"`
	Points       int64  `json:"points,omitempty" jsonschema:"可选：顺便赠送积分"`
	Remark       string `json:"remark,omitempty" jsonschema:"可选：管理员备注"`
}

type mcpUpsertPackageIn struct {
	ID           int64   `json:"id,omitempty" jsonschema:"套餐 ID；0 为新建，非 0 为修改"`
	Type         string  `json:"type,omitempty" jsonschema:"商品类型：plan（订阅套餐，默认）| traffic（流量包）"`
	Name         string  `json:"name" jsonschema:"套餐名称（必填）"`
	Description  string  `json:"description,omitempty" jsonschema:"商品描述"`
	PricePoints  int64   `json:"price_points,omitempty" jsonschema:"售价（积分），默认 0"`
	TrafficGB    float64 `json:"traffic_gb,omitempty" jsonschema:"流量上限（GB，例如 100 表示 100 GiB；与 traffic_bytes 二选一）"`
	TrafficBytes int64   `json:"traffic_bytes,omitempty" jsonschema:"精确流量上限（字节）"`
	DurationDays int64   `json:"duration_days,omitempty" jsonschema:"有效期天数（plan 必填正数，默认 30）"`
	MonthlyReset *bool   `json:"monthly_reset,omitempty" jsonschema:"是否每 30 天重置流量（plan 新建默认 true）"`
	Stock        *int64  `json:"stock,omitempty" jsonschema:"库存（-1 为无限，默认 -1）"`
	Enabled      *bool   `json:"enabled,omitempty" jsonschema:"是否上架（新建默认 true）"`
	GroupIDs     []int64 `json:"group_ids,omitempty" jsonschema:"绑定的节点分组 ID 列表（plan 类型必须绑定含可用节点的分组）"`
	UserGroupIDs []int64 `json:"user_group_ids,omitempty" jsonschema:"可购买用户组 ID 列表（留空表示所有人可购买）"`
}

type mcpCreateUserIn struct {
	Username string `json:"username" jsonschema:"用户名（3-32 位字母、数字或下划线）"`
	Password string `json:"password,omitempty" jsonschema:"密码（至少 6 位，留空自动随机生成）"`
	Email    string `json:"email,omitempty" jsonschema:"可选邮箱"`
	Points   int64  `json:"points,omitempty" jsonschema:"可选初始积分"`
	Remark   string `json:"remark,omitempty" jsonschema:"可选管理员备注"`
}

type mcpUpdateUserIn struct {
	ID            int64    `json:"id" jsonschema:"用户 ID（必填）"`
	Status        *string  `json:"status,omitempty" jsonschema:"账号状态：active | banned"`
	Password      *string  `json:"password,omitempty" jsonschema:"重置登录密码（至少 6 位，会踢出全部现有会话）"`
	Email         *string  `json:"email,omitempty" jsonschema:"修改或解绑邮箱（传空串解绑）"`
	Remark        *string  `json:"remark,omitempty" jsonschema:"修改管理员备注"`
	ResetTraffic  bool     `json:"reset_traffic,omitempty" jsonschema:"是否重置当前已用流量"`
	ManualEnabled *bool    `json:"manual_enabled,omitempty" jsonschema:"是否启用管理员手动额度"`
	ManualGB      *float64 `json:"manual_gb,omitempty" jsonschema:"管理员手动额度流量（GB）"`
	ManualExpiry  *int64   `json:"manual_expiry,omitempty" jsonschema:"管理员手动额度到期 Unix 时间戳（0 表示永不过期）"`
	GroupIDs      *[]int64 `json:"group_ids,omitempty" jsonschema:"用户组 ID 列表"`
}

type mcpAssignPlanIn struct {
	UserID       int64 `json:"user_id" jsonschema:"用户 ID"`
	PackageID    int64 `json:"package_id" jsonschema:"套餐 ID"`
	DurationDays int64 `json:"duration_days,omitempty" jsonschema:"可选时长（天），0 用套餐默认"`
}

type mcpAdjustTrafficIn struct {
	UserID     int64   `json:"user_id" jsonschema:"用户 ID"`
	PlanID     int64   `json:"plan_id" jsonschema:"用户套餐桶 ID（可先通过 list_users 或查看用户套餐获取）"`
	DeltaGB    float64 `json:"delta_gb,omitempty" jsonschema:"增减流量（GB，正数增加、负数扣减；与 delta_bytes 二选一）"`
	DeltaBytes int64   `json:"delta_bytes,omitempty" jsonschema:"精确增减流量（字节）"`
}

type mcpManageRegCodesIn struct {
	Action   string  `json:"action" jsonschema:"list | generate | toggle | delete"`
	ID       int64   `json:"id,omitempty" jsonschema:"注册码 ID（toggle/delete 使用）"`
	Count    int     `json:"count,omitempty" jsonschema:"generate 数量，默认 1"`
	MaxUses  int64   `json:"max_uses,omitempty" jsonschema:"单个注册码最大使用次数（0 表示不限，默认 1）"`
	Note     string  `json:"note,omitempty" jsonschema:"备注"`
	GroupIDs []int64 `json:"group_ids,omitempty" jsonschema:"使用该注册码后自动加入的用户组 ID"`
	Enabled  *bool   `json:"enabled,omitempty" jsonschema:"切换启停状态时的目标值"`
}

type mcpManageAnnouncementsIn struct {
	Action  string `json:"action" jsonschema:"list | upsert | delete"`
	ID      int64  `json:"id,omitempty" jsonschema:"公告 ID"`
	Title   string `json:"title,omitempty" jsonschema:"公告标题"`
	Content string `json:"content,omitempty" jsonschema:"公告正文（支持 Markdown）"`
	Pinned  bool   `json:"pinned,omitempty" jsonschema:"是否置顶"`
	Enabled *bool  `json:"enabled,omitempty" jsonschema:"是否启用（新建默认 true）"`
}

func (a *API) registerMCPUserTools(srv *mcp.Server) {
	// 1. provision_user_access (workflow tool)
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "provision_user_access",
		Description: "【推荐】一站式开通用户订阅：自动查找或创建用户（免邮箱验证阻塞）、开通代理身份、分配指定套餐，并立即校验该用户实际可见节点数与订阅链接。若可见节点为 0 会直接说明原因。",
	}, "mcp:users", func(_ context.Context, auth mcpAuthInfo, in mcpProvisionUserAccessIn) (any, error) {
		return a.mcpProvisionUserAccess(auth, in)
	})

	// 2. upsert_package
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "upsert_package",
		Description: "新建或更新套餐/流量包，支持直接按 GB 设置流量并绑定节点分组（group_ids）。",
	}, "mcp:users", func(_ context.Context, _ mcpAuthInfo, in mcpUpsertPackageIn) (any, error) {
		return a.mcpUpsertPackage(in)
	})

	// 3. retire_package
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "retire_package",
		Description: "下架套餐并为在用订阅者按策略退款清空（两段确认：confirm=false 仅返回受影响订阅人数预览）。",
	}, "mcp:users", func(_ context.Context, auth mcpAuthInfo, in mcpConfirmIDIn) (any, error) {
		pkg, err := a.st.GetPackage(in.ID)
		if err != nil || pkg == nil {
			return nil, errors.New("套餐不存在")
		}
		subs, _ := a.st.PackagePlanHolders(in.ID)
		if !in.Confirm {
			return J{
				"dry_run":     true,
				"package_id":  pkg.ID,
				"name":        pkg.Name,
				"subscribers": len(subs),
				"message":     fmt.Sprintf("即将下架套餐「%s」，当前有 %d 位订阅者将被按比例退款并清空该套餐。请向用户确认后传 confirm=true 执行。", pkg.Name, len(subs)),
			}, nil
		}
		refunded, cleared := 0, 0
		for _, uid := range subs {
			orders, _ := a.st.RefundableOrdersForPackage(uid, in.ID)
			for _, oid := range orders {
				if _, _, err := a.st.RefundOrder(oid, auth.UserID, "", a.syncEntitlement); err == nil {
					refunded++
				}
			}
			if err := a.st.ClearPlanBucket(uid, in.ID); err == nil {
				cleared++
			}
			a.invalidateLinks(uid)
		}
		if err := a.st.SetPackageEnabled(in.ID, false); err != nil {
			return nil, err
		}
		a.sbRebuildLog()
		return J{"retired": true, "package_id": in.ID, "subscribers": len(subs), "refunded": refunded, "cleared": cleared}, nil
	})

	// 4. create_user
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "create_user",
		Description: "管理员直接开户（自动免邮箱验证并开通代理凭据）。密码留空时自动生成随机密码并在本次返回。",
	}, "mcp:users", func(_ context.Context, auth mcpAuthInfo, in mcpCreateUserIn) (any, error) {
		u, genPass, err := a.mcpCreateUserInternal(auth.UserID, in)
		if err != nil {
			return nil, err
		}
		res := J{"user": a.adminUserViewLoadGroups(u)}
		if genPass != "" {
			res["generated_password"] = genPass
		}
		return res, nil
	})

	// 5. update_user
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "update_user",
		Description: "编辑用户状态（active/banned）、重置密码、改邮箱/备注、重置流量、设置管理员手动额度或用户组（不支持提升角色为管理员）。",
	}, "mcp:users", func(_ context.Context, _ mcpAuthInfo, in mcpUpdateUserIn) (any, error) {
		return a.mcpUpdateUser(in)
	})

	// 6. assign_user_plan
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "assign_user_plan",
		Description: "给用户直接分配指定套餐（不扣积分），并刷新 sing-box 配置与订阅缓存。",
	}, "mcp:users", func(_ context.Context, auth mcpAuthInfo, in mcpAssignPlanIn) (any, error) {
		u, err := a.mcpAssignPlanInternal(auth.UserID, in.UserID, in.PackageID, in.DurationDays)
		if err != nil {
			return nil, err
		}
		entries := a.computeNodeEntries(u)
		return J{
			"user":                a.adminUserViewLoadGroups(u),
			"visible_node_count":  len(entries),
			"subscription_url":    a.subURL(nil, a.ensureSubToken(u)),
		}, nil
	})

	// 7. adjust_user_traffic
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "adjust_user_traffic",
		Description: "对用户的某份套餐桶增加或扣减流量（可传 delta_gb 或 delta_bytes）。",
	}, "mcp:users", func(_ context.Context, _ mcpAuthInfo, in mcpAdjustTrafficIn) (any, error) {
		if in.UserID <= 0 || in.PlanID <= 0 {
			return nil, errors.New("user_id 与 plan_id 必填")
		}
		delta := in.DeltaBytes
		if delta == 0 && in.DeltaGB != 0 {
			delta = int64(in.DeltaGB * float64(mcpGiB))
		}
		if delta == 0 {
			return nil, errors.New("delta_gb 或 delta_bytes 不能为 0")
		}
		b, err := a.st.AdjustBucketTraffic(in.UserID, in.PlanID, delta)
		if err != nil {
			return nil, err
		}
		a.invalidateLinks(in.UserID)
		a.sbRebuildLog()
		return J{"id": b.ID, "name": b.Name, "kind": b.Kind, "traffic_limit": b.TrafficLimit, "used": b.Used()}, nil
	})

	// 8. reset_user_node_creds
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "reset_user_node_creds",
		Description: "重置用户节点连接凭据以吊销已泄露的节点配置（两段确认：会触发相关机器重载 sing-box）。",
	}, "mcp:users", func(_ context.Context, _ mcpAuthInfo, in mcpConfirmIDIn) (any, error) {
		u, err := a.st.UserByID(in.ID)
		if err != nil || u == nil {
			return nil, errors.New("用户不存在")
		}
		if !in.Confirm {
			return J{"dry_run": true, "user_id": u.ID, "username": u.Username, "message": fmt.Sprintf("即将重置用户「%s」的节点凭据，旧订阅导出的节点将全部失效且会重载 sing-box。请确认后传 confirm=true 执行。", u.Username)}, nil
		}
		if err := a.st.RotateNodeCredentials(in.ID, store.NoCredsResetCooldown); err != nil {
			return nil, err
		}
		a.invalidateLinks(in.ID)
		a.sbRebuildLog()
		return J{"rotated": true, "user_id": in.ID, "username": u.Username}, nil
	})

	// 9. delete_user
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "delete_user",
		Description: "删除普通用户账号（两段确认；禁止删除管理员账号）。",
	}, "mcp:users", func(_ context.Context, _ mcpAuthInfo, in mcpConfirmIDIn) (any, error) {
		u, err := a.st.UserByID(in.ID)
		if err != nil || u == nil {
			return nil, errors.New("用户不存在")
		}
		if u.Role == "admin" {
			return nil, errors.New("不能删除管理员账号")
		}
		if !in.Confirm {
			return J{"dry_run": true, "user_id": u.ID, "username": u.Username, "message": fmt.Sprintf("即将永久删除用户「%s」，请向用户确认后传 confirm=true 执行。", u.Username)}, nil
		}
		if err := a.st.DeleteUser(in.ID); err != nil {
			return nil, err
		}
		a.invalidateLinks(in.ID)
		a.sbRebuildLog()
		return J{"deleted": true, "user_id": in.ID, "username": u.Username}, nil
	})

	// 10. manage_reg_codes
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "manage_reg_codes",
		Description: "管理邀请注册码：list（查询）、generate（批量生成）、toggle（启停）、delete（删除）。",
	}, "mcp:users", func(_ context.Context, _ mcpAuthInfo, in mcpManageRegCodesIn) (any, error) {
		switch strings.ToLower(strings.TrimSpace(in.Action)) {
		case "list", "":
			list, err := a.st.ListRegCodes()
			if err != nil {
				return nil, err
			}
			return J{"reg_codes": list}, nil
		case "generate":
			cnt := in.Count
			if cnt <= 0 {
				cnt = 1
			}
			maxUses := in.MaxUses
			if maxUses < 0 {
				maxUses = 1
			}
			codes, err := a.st.GenerateRegCodes(cnt, maxUses, strings.TrimSpace(in.Note), in.GroupIDs)
			if err != nil {
				return nil, err
			}
			return J{"codes": codes}, nil
		case "toggle":
			if in.ID <= 0 || in.Enabled == nil {
				return nil, errors.New("toggle 需要 id 与 enabled")
			}
			if err := a.st.SetRegCodeEnabled(in.ID, *in.Enabled); err != nil {
				return nil, err
			}
			return J{"id": in.ID, "enabled": *in.Enabled}, nil
		case "delete":
			if in.ID <= 0 {
				return nil, errors.New("delete 需要 id")
			}
			if err := a.st.DeleteRegCode(in.ID); err != nil {
				return nil, err
			}
			return J{"deleted": true, "id": in.ID}, nil
		default:
			return nil, errors.New("action 必须为 list | generate | toggle | delete")
		}
	})

	// 11. manage_announcements
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "manage_announcements",
		Description: "管理站点公告：list（列出全部）、upsert（新建或更新）、delete（删除）。",
	}, "mcp:users", func(_ context.Context, _ mcpAuthInfo, in mcpManageAnnouncementsIn) (any, error) {
		switch strings.ToLower(strings.TrimSpace(in.Action)) {
		case "list", "":
			list, err := a.st.ListAnnouncements(false)
			if err != nil {
				return nil, err
			}
			return J{"announcements": list}, nil
		case "upsert":
			if strings.TrimSpace(in.Title) == "" {
				return nil, errors.New("公告标题不能为空")
			}
			en := true
			if in.Enabled != nil {
				en = *in.Enabled
			}
			an := store.Announcement{ID: in.ID, Title: strings.TrimSpace(in.Title), Content: in.Content, Pinned: in.Pinned, Enabled: en}
			if in.ID == 0 {
				id, err := a.st.CreateAnnouncement(an)
				if err != nil {
					return nil, err
				}
				return a.st.GetAnnouncement(id)
			}
			if err := a.st.UpdateAnnouncement(an); err != nil {
				return nil, err
			}
			return a.st.GetAnnouncement(in.ID)
		case "delete":
			if in.ID <= 0 {
				return nil, errors.New("delete 需要 id")
			}
			if err := a.st.DeleteAnnouncement(in.ID); err != nil {
				return nil, err
			}
			return J{"deleted": true, "id": in.ID}, nil
		default:
			return nil, errors.New("action 必须为 list | upsert | delete")
		}
	})
}

// ---- Implementations ----

func (a *API) mcpProvisionUserAccess(auth mcpAuthInfo, in mcpProvisionUserAccessIn) (any, error) {
	if in.PackageID <= 0 {
		return nil, errors.New("package_id 必填")
	}
	var u *store.User
	var genPass string
	var err error
	if in.UserID > 0 {
		u, err = a.st.UserByID(in.UserID)
		if err != nil || u == nil {
			return nil, errors.New("指定 user_id 不存在")
		}
	} else if name := strings.TrimSpace(in.Username); name != "" {
		u, _ = a.st.UserByUsername(name)
		if u == nil {
			u, genPass, err = a.mcpCreateUserInternal(auth.UserID, mcpCreateUserIn{
				Username: name,
				Password: in.Password,
				Email:    in.Email,
				Points:   in.Points,
				Remark:   in.Remark,
			})
			if err != nil {
				return nil, err
			}
		}
	} else {
		return nil, errors.New("必须提供 user_id 或 username")
	}

	u, err = a.mcpAssignPlanInternal(auth.UserID, u.ID, in.PackageID, in.DurationDays)
	if err != nil {
		return nil, err
	}
	u = a.ensureSubToken(u)
	entries := a.computeNodeEntries(u)
	proxies := a.st.BuildUserProxies(u, a.nodeHost())
	subURL := a.subURL(nil, u)

	warnings := []string{}
	if len(entries) == 0 && len(proxies) == 0 {
		if a.nodeHost() == "" {
			warnings = append(warnings, "当前可用节点为 0：未配置本机「节点对外地址」(node_host_override) 且无已启用远程服务器。")
		} else {
			warnings = append(warnings, "当前可用节点为 0：请检查该套餐是否已绑定包含已启用节点的节点分组（可调用 diagnose_panel 查看具体原因）。")
		}
	}
	if a.siteBase() == "" {
		warnings = append(warnings, "尚未配置面板访问地址 (public_base)，订阅链接仅包含相对路径，建议通过 update_settings 补全 public_base。")
	}

	res := J{
		"user":               a.adminUserViewLoadGroups(u),
		"subscription_url":   subURL,
		"visible_node_count": len(entries),
		"proxy_count":        len(proxies),
		"warnings":           warnings,
	}
	if genPass != "" {
		res["generated_password"] = genPass
	}
	return res, nil
}

func (a *API) mcpUpsertPackage(in mcpUpsertPackageIn) (any, error) {
	var p store.Package
	if in.ID > 0 {
		cur, err := a.st.GetPackage(in.ID)
		if err != nil || cur == nil {
			return nil, errors.New("套餐不存在")
		}
		p = *cur
	} else {
		p = store.Package{
			Type:         "plan",
			DurationDays: 30,
			MonthlyReset: true,
			Stock:        -1,
			Enabled:      true,
		}
	}
	if strings.TrimSpace(in.Type) != "" {
		p.Type = strings.TrimSpace(in.Type)
	}
	if strings.TrimSpace(in.Name) != "" {
		p.Name = strings.TrimSpace(in.Name)
	}
	if in.Description != "" {
		p.Description = in.Description
	}
	if in.ID == 0 || in.PricePoints != 0 {
		p.PricePoints = in.PricePoints
	}
	if in.TrafficBytes > 0 {
		p.TrafficBytes = in.TrafficBytes
	} else if in.TrafficGB > 0 {
		p.TrafficBytes = int64(in.TrafficGB * float64(mcpGiB))
	}
	if in.DurationDays > 0 {
		p.DurationDays = in.DurationDays
	}
	if in.MonthlyReset != nil {
		p.MonthlyReset = *in.MonthlyReset
	}
	if in.Stock != nil {
		p.Stock = *in.Stock
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	p.NormalizeMonthly()
	if msg := validatePackage(&p); msg != "" {
		return nil, errors.New(msg)
	}
	var id int64
	if in.ID == 0 {
		var err error
		id, err = a.st.CreatePackage(p)
		if err != nil {
			return nil, err
		}
	} else {
		id = in.ID
		p.ID = id
		if err := a.st.UpdatePackage(p); err != nil {
			return nil, err
		}
	}
	if p.Type == "plan" && in.GroupIDs != nil {
		if err := a.st.SetPlanGroups(id, in.GroupIDs); err != nil {
			return nil, err
		}
	}
	if in.UserGroupIDs != nil {
		if err := a.st.SetPackageUserGroups(id, in.UserGroupIDs); err != nil {
			return nil, err
		}
	}
	a.invalidateAllLinks()
	a.sbRebuildLog()
	saved, _ := a.st.GetPackage(id)
	if saved != nil {
		if saved.Type == "plan" {
			saved.GroupIDs, _ = a.st.PlanGroupIDs(id)
		}
		saved.UserGroupIDs, _ = a.st.PackageUserGroupIDs(id)
	}
	return saved, nil
}

func (a *API) mcpCreateUserInternal(operatorID int64, in mcpCreateUserIn) (*store.User, string, error) {
	username := strings.TrimSpace(in.Username)
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if !usernameRe.MatchString(username) {
		return nil, "", errors.New("用户名需为 3-32 位字母、数字或下划线")
	}
	pw := in.Password
	genPass := ""
	if pw == "" {
		tok, err := idgen.RandToken(9)
		if err != nil {
			return nil, "", err
		}
		pw = tok
		genPass = tok
	}
	if len(pw) < 6 {
		return nil, "", errors.New("密码至少 6 位")
	}
	if email != "" && !validEmail(email) {
		return nil, "", errors.New("邮箱格式不正确")
	}
	if exist, _ := a.st.UserByUsername(username); exist != nil {
		return nil, "", errors.New("用户名已存在")
	}
	if email != "" {
		if exist, _ := a.st.UserByEmail(email); exist != nil {
			return nil, "", errors.New("邮箱已被使用")
		}
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return nil, "", err
	}
	subToken, err := idgen.RandToken(24)
	if err != nil {
		return nil, "", err
	}
	id, err := a.st.CreateUser(store.NewUser{
		Username:        username,
		Email:           email,
		PasswordHash:    hash,
		SubToken:        subToken,
		EmailGateExempt: true,
	})
	if err != nil {
		return nil, "", err
	}
	_ = a.st.SetEmailVerified(id)
	u, err := a.st.UserByID(id)
	if err != nil || u == nil {
		return nil, "", errors.New("读取新用户失败")
	}
	if a.sbctl != nil {
		if err := a.provisionClient(u); err != nil {
			_ = a.st.DeleteUser(id)
			return nil, "", fmt.Errorf("开通节点凭据失败: %w", err)
		}
	}
	if strings.TrimSpace(in.Remark) != "" {
		_ = a.st.SetUserRemark(id, truncateRunesAPI(strings.TrimSpace(in.Remark), maxRemarkRunes))
	}
	if in.Points > 0 {
		_, _ = a.st.AdjustPoints(id, in.Points, "admin_recharge", operatorID, "AI MCP 开户赠送")
	}
	u, _ = a.st.UserByID(id)
	return u, genPass, nil
}

func (a *API) mcpUpdateUser(in mcpUpdateUserIn) (any, error) {
	u, err := a.st.UserByID(in.ID)
	if err != nil || u == nil {
		return nil, errors.New("用户不存在")
	}
	if in.Password != nil && *in.Password != "" && len(*in.Password) < 6 {
		return nil, errors.New("密码至少 6 位")
	}
	status := u.Status
	if in.Status != nil {
		if *in.Status != "active" && *in.Status != "banned" {
			return nil, errors.New("status 只能为 active | banned")
		}
		status = *in.Status
	}
	if in.Email != nil {
		ne := strings.TrimSpace(strings.ToLower(*in.Email))
		if ne != "" {
			if !validEmail(ne) {
				return nil, errors.New("邮箱格式不正确")
			}
			if other, _ := a.st.UserByEmail(ne); other != nil && other.ID != in.ID {
				return nil, errors.New("邮箱已被其他用户绑定")
			}
		}
		if err := a.st.AdminSetUserEmail(in.ID, ne); err != nil {
			return nil, err
		}
	}
	if in.Password != nil && *in.Password != "" {
		hash, err := auth.HashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		if err := a.st.UpdatePassword(in.ID, hash); err != nil {
			return nil, err
		}
		_ = a.st.DeleteUserSessions(in.ID)
	}
	if in.Remark != nil {
		r := strings.TrimSpace(*in.Remark)
		if len([]rune(r)) > maxRemarkRunes {
			return nil, errors.New("备注最多 200 字")
		}
		if err := a.st.SetUserRemark(in.ID, r); err != nil {
			return nil, err
		}
	}
	var manual *store.ManualGrant
	if in.ManualEnabled != nil {
		g := store.ManualGrant{Enabled: *in.ManualEnabled}
		if in.ManualGB != nil && *in.ManualGB > 0 {
			g.Traffic = int64(*in.ManualGB * float64(mcpGiB))
		}
		if in.ManualExpiry != nil && *in.ManualExpiry >= 0 {
			g.Expiry = *in.ManualExpiry
		}
		if g.Enabled && g.Traffic <= 0 {
			return nil, errors.New("启用管理员额度时 manual_gb 必须大于 0")
		}
		manual = &g
	}
	if err := a.st.AdminUpdateUser(in.ID, status, in.ResetTraffic, manual); err != nil {
		return nil, err
	}
	if in.GroupIDs != nil {
		if err := a.st.SetUserGroups(in.ID, *in.GroupIDs); err != nil {
			return nil, err
		}
	}
	if status == "banned" {
		_ = a.st.DeleteUserSessions(in.ID)
	}
	a.invalidateLinks(in.ID)
	a.sbRebuildLog()
	out, _ := a.st.UserByID(in.ID)
	return a.adminUserViewLoadGroups(out), nil
}

func (a *API) mcpAssignPlanInternal(operatorID, userID, packageID, durationDays int64) (*store.User, error) {
	u, err := a.st.UserByID(userID)
	if err != nil || u == nil {
		return nil, errors.New("用户不存在")
	}
	pkg, err := a.st.GetPackage(packageID)
	if err != nil || pkg == nil {
		return nil, errors.New("套餐不存在")
	}
	if !u.ClientID.Valid && a.sbctl != nil {
		if err := a.provisionClient(u); err != nil {
			return nil, fmt.Errorf("开通节点凭据失败: %w", err)
		}
		u, _ = a.st.UserByID(userID)
	}
	if _, err := a.st.AssignPackageDuration(userID, pkg, durationDays, operatorID, a.syncEntitlement); err != nil {
		return nil, err
	}
	a.invalidateLinks(userID)
	a.sbRebuildLog()
	u, _ = a.st.UserByID(userID)
	return u, nil
}

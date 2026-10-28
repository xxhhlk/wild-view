package loomy

// snapBody 默认快照的 data 段（活动开启、业务日 2026-10-17、三档、今日未领）。
const snapBody = `{"enabled":true,"businessDate":"2026-10-17","config":{` +
	`"enabled":true,"businessDate":"2026-10-17","configVersion":"pet-work-v1","dailyMaxPoints":1000,` +
	`"tiers":[{"tierId":"tier_1","score":60},{"tierId":"tier_2","score":100},{"tierId":"tier_3","score":150}]},` +
	`"status":{"earnedPointsToday":0,"rewardedTierIds":[],"dailyRewardCapped":false}}`

// snapJSON 默认快照（带信封）。上游统一信封是 {code,desc,data}，data 才是快照本体
// （证据：integration test 的 sendJson({code:'000000',data:{...}}) 与 points-service.js:208 的 code 判定）。
var snapJSON = okEnvelope(snapBody)

// okEnvelope 上游成功信封（code 必须是字符串 "000000"，见 points-service.js:208）。
func okEnvelope(data string) string { return `{"code":"000000","desc":"","data":` + data + `}` }

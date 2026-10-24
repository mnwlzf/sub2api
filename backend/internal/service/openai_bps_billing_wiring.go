package service

// OpenAIBPSAccountStateStoreFromRepository 是生产接线辅助：AccountRepository 接口
// 本身不包含 BPS 的可选能力方法（SetRateLimited/SetModelRateLimit 在接口里，
// MarkOpenAIBPSAuthFailure 只在具体仓储实现上），因此这里做一次运行期断言，
// 避免 wire provider 被迫依赖具体仓储类型。
//
// 未实现的能力按 nil 处理：对应写入退化为 no-op，绝不 panic。
func OpenAIBPSAccountStateStoreFromRepository(repo AccountRepository) *OpenAIBPSAccountStateStore {
	store := &OpenAIBPSAccountStateStore{}
	if repo == nil {
		return store
	}
	if accounts, ok := repo.(OpenAIBPSAccountStateRepository); ok {
		store.accounts = accounts
	}
	if credentials, ok := repo.(OpenAIBPSCredentialFailureWriter); ok {
		store.credentials = credentials
	}
	return store
}

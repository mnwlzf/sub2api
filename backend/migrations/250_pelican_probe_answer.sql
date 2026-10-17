-- 250_pelican_probe_answer.sql
-- 糖果题确定性判分的解析结果。
--
-- 背景：糖果题此前完全依赖 LLM 裁判，pelican_probe_results.note 里只留下
-- "judge=...: YES/NO"，模型答错的数字被丢弃，无法统计「错在哪」。提示词改为
-- 要求最后单独输出 "ANSWER: <数字>" 之后，主判分路径变成确定性解析
-- （见 pelican_probe_candy.go），解析出的数字落到这一列。
--
-- answer_value 可空：非糖果题、解析失败（无 ANSWER 行 / 多行冲突 / 交给裁判
-- 兜底）以及本次改动之前的历史结果都没有该值。纯加性变更，回滚安全。

ALTER TABLE pelican_probe_results ADD COLUMN IF NOT EXISTS answer_value INT;

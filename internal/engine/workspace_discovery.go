package engine

import "fmt"

func validateWorkspaceQueue(items []workspaceRecord, id, queue string) error {
	if queue == "" {
		return nil
	}
	for _, w := range items {
		if w.ID != id && sameRoot(queue, w.QueuePath) {
			return fmt.Errorf("実行データの保存先がワークスペース「%s」と重複しています。ワークスペースを作り直してください", w.Name)
		}
	}
	return nil
}

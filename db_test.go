package barrage

import "testing"

func TestIsReadQuery(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"SELECT count(*) FROM orders", true},
		{"  select customer from orders", true},
		{"SHOW TABLES", true},
		{"EXPLAIN ANALYZE SELECT 1", true},
		{"INSERT INTO orders (customer, amount) VALUES ('x', 1)", false},
		{"UPDATE orders SET amount = 2 WHERE id = 1", false},
		{"DELETE FROM orders WHERE id = 1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isReadQuery(c.query); got != c.want {
			t.Errorf("isReadQuery(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

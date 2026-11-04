//go:build !race

package readmodel_test

import "time"

// Con el detector de carreras todo va varias veces más lento.
const deleteBudget = 5 * time.Second

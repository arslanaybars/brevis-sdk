package sdk

import (
	"github.com/consensys/gnark-crypto/ecc"
	"math/big"
)

type DataStream[T any] struct {
	api        *CircuitAPI
	underlying []T
	toggles    []Variable
	max        int
}

func NewDataStream[T any](api *CircuitAPI, in DataPoints[T]) *DataStream[T] {
	return &DataStream[T]{
		api:        api,
		underlying: in.Raw,
		toggles:    in.Toggles,
		max:        NumMaxDataPoints, // TODO allow developer to customize max
	}
}

func newDataStream[T any](api *CircuitAPI, in []T, toggles []Variable, max int) *DataStream[T] {
	return &DataStream[T]{
		api:        api,
		underlying: in,
		toggles:    toggles,
		max:        max,
	}
}

// Get gets an element from the data stream. Performed on the underlying data
// directly. It also requires the underlying data slot is valid
func (ds *DataStream[T]) Get(index int) T {
	v := ds.underlying[index]
	t := ds.toggles[index]
	ds.api.AssertIsEqual(t, 1)
	return v
}

// Range selects a range of the data stream. Performed on the underlying data directly.
func (ds *DataStream[T]) Range(start, end int) *DataStream[T] {
	return newDataStream(ds.api, ds.underlying[start:end], ds.toggles[start:end], end-start)
}

type MapFunc[T any] func(current T) Variable

// Map calls the input mapFunc on every valid element in the stream
func (ds *DataStream[T]) Map(mapFunc MapFunc[T]) *DataStream[Variable] {
	res := make([]Variable, ds.max)
	for i, data := range ds.underlying {
		res[i] = mapFunc(data)
	}
	return newDataStream(ds.api, res, ds.toggles, ds.max)
}

type Map2Func[T any] func(current T) [2]Variable

// Map2 is like Map but maps every element to two variables
func (ds *DataStream[T]) Map2(mapFunc Map2Func[T]) *DataStream[[2]Variable] {
	res := make([][2]Variable, ds.max)
	for i, data := range ds.underlying {
		res[i] = mapFunc(data)
	}
	return newDataStream(ds.api, res, ds.toggles, ds.max)
}

type AssertFunc[T any] func(current T) Variable

// AssertEach performs the standard api.AssertIsEqual on every valid element of the stream
func (ds *DataStream[T]) AssertEach(assertFunc AssertFunc[T]) {
	for i, data := range ds.underlying {
		pass := assertFunc(data)
		valid := ds.api.Equal(ds.toggles[i], 1)
		pass = ds.api.Select(valid, pass, 1)
		ds.api.AssertIsEqual(pass, 1)
	}
}

// SortFunc returns 1 if a, b are sorted, 0 if not.
type SortFunc func(a, b Variable) Variable

// IsSorted returns 1 if the data stream is sorted to the criteria of sortFunc, 0 if not.
func (ds *DataStream[T]) IsSorted(getValue GetValueFunc[T], sortFunc SortFunc) Variable {
	// The following code uses prev and prevValid to pass the signal of last known
	// valid element of the data stream. This is needed because the stream could have
	// already been filtered, meaning we could have "gaps" between valid elements
	//
	//TODO:
	// we could use a bool in ds to indicate whether the toggles this ds has been
	// touched (the stream has been filtered) before this part of the user circuit
	// where this method is called. if it has not been touched, we probably don't
	// need to use prev and prevValid signals.
	api := ds.api
	var sorted Variable
	prev := getValue(ds.underlying[0])
	prevValid := ds.toggles[0]

	for i := 1; i < ds.max; i++ {
		curr := getValue(ds.underlying[i])
		currValid := ds.toggles[i]

		sorted = sortFunc(prev, curr)
		sorted = api.API.Select(api.API.And(prevValid, currValid), sorted, 1)

		prev = api.Select(currValid, curr, prev)
		prevValid = currValid
	}
	return sorted
}

// AssertSorted Performs the sortFunc on each valid pair of data points and assert the result to be 1.
func (ds *DataStream[T]) AssertSorted(getValue GetValueFunc[T], sortFunc SortFunc) {
	ds.api.AssertIsEqual(ds.IsSorted(getValue, sortFunc), 1)
}

// Count returns the number of valid elements (i.e. toggled on) in the data stream.
func (ds *DataStream[T]) Count() Variable {
	t := ds.toggles
	count := ds.api.API.Add(t[0], t[1], t[2:]...) // Todo: cache this signal in case it's used more than once
	return count
}

type ReduceFunc[T any] func(accumulator Variable, current T) (newAccumulator Variable)

// Reduce reduces the data stream to a single circuit variable
func (ds *DataStream[T]) Reduce(initial Variable, reduceFunc ReduceFunc[T]) Variable {
	var acc = initial
	for i, data := range ds.underlying {
		newAcc := reduceFunc(acc, data)
		acc = ds.api.Select(ds.toggles[i], newAcc, acc)
	}
	return acc
}

type Reduce2Func[T any] func(accumulator [2]Variable, current T) (newAccumulator [2]Variable)

// Reduce2 works the same way as Reduce but works on two elements
func (ds *DataStream[T]) Reduce2(initial [2]Variable, reduceFunc Reduce2Func[T]) [2]Variable {
	api := ds.api
	acc := initial
	for i, data := range ds.underlying {
		newAcc := reduceFunc(acc, data)
		valid := api.Equal(ds.toggles[i], 1)
		acc[0] = api.Select(valid, newAcc[0], acc[0])
		acc[1] = api.Select(valid, newAcc[1], acc[1])
	}
	return acc
}

// FilterFunc must return 1/0 to include/exclude `current` in the filter result
type FilterFunc[T any] func(current T) Variable

// Filter filters the data stream with a user-supplied filterFunc
// Internally it toggles off the elements that does not meet the filter criteria
func (ds *DataStream[T]) Filter(filterFunc FilterFunc[T]) *DataStream[T] {
	newToggles := make([]Variable, ds.max)
	for i, data := range ds.underlying {
		toggle := filterFunc(data)
		valid := ds.api.Equal(ds.toggles[i], 1)
		newToggles[i] = ds.api.API.Select(ds.api.API.And(toggle, valid), 1, 0)
	}
	return newDataStream(ds.api, ds.underlying, newToggles, ds.max)
}

type GetValueFunc[T any] func(current T) Variable

// Min finds out the minimum value of the selected field from the data stream. Uses Reduce under the hood.
func (ds *DataStream[T]) Min(getValue GetValueFunc[T]) Variable {
	maxInt := new(big.Int).Sub(ecc.BLS12_377.ScalarField(), big.NewInt(1))
	return ds.Reduce(maxInt, func(min Variable, current T) (newMin Variable) {
		curr := getValue(current)
		curLtMin := ds.api.LT(curr, min)
		return ds.api.Select(curLtMin, curr, min)
	})
}

// Max finds out the maximum value of the selected field from the data stream. Uses Reduce under the hood.
func (ds *DataStream[T]) Max(getValue GetValueFunc[T]) Variable {
	return ds.Reduce(0, func(max Variable, current T) (newMax Variable) {
		curr := getValue(current)
		curGtMax := ds.api.GT(curr, max)
		return ds.api.Select(curGtMax, curr, max)
	})
}

// Sum sums values of the selected field in the data stream. Uses Reduce.
func (ds *DataStream[T]) Sum(getValue GetValueFunc[T]) Variable {
	return ds.Reduce(0, func(sum Variable, current T) (newSum Variable) {
		curr := getValue(current)
		return ds.api.Add(sum, curr)
	})
}

// Mean calculates the arithmetic mean over the selected fields of the data stream. Uses Sum.
func (ds *DataStream[T]) Mean(getValue GetValueFunc[T]) Variable {
	sum := ds.Sum(getValue)
	return ds.api.Div(sum, ds.Count())
}

// StdDev calculates the standard deviation over the selected fields of the data stream. Uses Mean and Sum.
// Uses the formula: 𝛔 = sqrt(Σ(x_i - μ)^2 / N)
func (ds *DataStream[T]) StdDev(getValue GetValueFunc[T]) Variable {
	mu := ds.Mean(getValue)
	n := ds.Count()

	// compute k = Σ(x_i - μ)^2
	k := ds.Reduce(0, func(acc Variable, current T) Variable {
		x := getValue(current)
		r := ds.api.Sub(x, mu)
		r2 := ds.api.Mul(r, r)
		return ds.api.Add(acc, r2)
	})

	return ds.api.Sqrt(ds.api.Div(k, n))
}

func (ds *DataStream[T]) Partition(n int) *DataStream[Tuple[T]] {
	l := len(ds.underlying)
	var ret []Tuple[T]
	for i := 0; i < l-n; i += n {
		start := i
		end := start + n
		if end > l {
			end = l
		}
		ret = append(ret, ds.underlying[start:end])
	}
	return newDataStream(ds.api, ret, ds.toggles, ds.max)
}

package sqladapter

import (
	"fmt"
	"reflect"

	"upper.io/db.v3"
	"upper.io/db.v3/internal/sqladapter/exql"
	"upper.io/db.v3/lib/reflectx"
)

var mapper = reflectx.NewMapper("db")

// Collection represents a SQL table.
type Collection interface {
	PartialCollection
	BaseCollection
}

// PartialCollection defines methods that must be implemented by the adapter.
type PartialCollection interface {
	// Database returns the parent database.
	Database() Database

	// Name returns the name of the table.
	Name() string

	// FilterConds filters the given conditions and transforms them if necessary.
	FilterConds(...interface{}) []interface{}

	// Insert inserts a new item into the collection.
	Insert(interface{}) (interface{}, error)
}

// BaseCollection provides logic for methods that can be shared across all SQL
// adapters.
type BaseCollection interface {
	// Exists returns true if the collection exists.
	Exists() bool

	// Find creates and returns a new result set.
	Find(conds ...interface{}) db.Result

	// Truncate removes all items on the collection.
	Truncate() error

	// InsertReturning inserts a new item and updates it with the
	// actual values from the database.
	InsertReturning(interface{}) error

	// PrimaryKeys returns the table's primary keys.
	PrimaryKeys() []string
}

// collection is the implementation of Collection.
type collection struct {
	BaseCollection
	PartialCollection

	pk []string
}

var (
	_ = Collection(&collection{})
)

// NewBaseCollection returns a collection with basic methods.
func NewBaseCollection(p PartialCollection) BaseCollection {
	c := &collection{PartialCollection: p}
	c.pk, _ = c.Database().PrimaryKeys(c.Name())
	return c
}

// PrimaryKeys returns the collection's primary keys, if any.
func (c *collection) PrimaryKeys() []string {
	return c.pk
}

// Find creates a result set with the given conditions.
func (c *collection) Find(conds ...interface{}) db.Result {
	return NewResult(
		c.Database(),
		c.Name(),
		c.FilterConds(conds...),
	)
}

// Exists returns true if the collection exists.
func (c *collection) Exists() bool {
	if err := c.Database().TableExists(c.Name()); err != nil {
		return false
	}
	return true
}

// InsertReturning inserts an item and updates the given variable reference.
func (c *collection) InsertReturning(item interface{}) error {
	if item == nil || reflect.TypeOf(item).Kind() != reflect.Ptr {
		return fmt.Errorf("Expecting a pointer but got %T", item)
	}

	var tx DatabaseTx
	inTx := false

	if currTx := c.Database().Transaction(); currTx != nil {
		tx = NewDatabaseTx(c.Database())
		inTx = true
	} else {
		// Not within a transaction, let's create one.
		var err error
		tx, err = c.Database().NewDatabaseTx(c.Database().Context())
		if err != nil {
			return err
		}
		defer tx.(Database).Close()
	}

	// Allocate a clone of item.
	newItem := reflect.New(reflect.ValueOf(item).Elem().Type()).Interface()
	var newItemFieldMap map[string]reflect.Value

	itemValue := reflect.ValueOf(item)

	col := tx.(Database).Collection(c.Name())

	// Insert item as is and grab the returning ID.
	id, err := col.Insert(item)
	if err != nil {
		goto cancel
	}
	if id == nil {
		err = fmt.Errorf("InsertReturning: Could not get a valid ID after inserting. Does the %q table have a primary key?", c.Name())
		goto cancel
	}

	// Fetch the row that was just interted into newItem
	if err = col.Find(id).One(newItem); err != nil {
		goto cancel
	}

	// Get valid fields from newItem to overwrite those that are on item.
	newItemFieldMap = mapper.ValidFieldMap(reflect.ValueOf(newItem))
	for fieldName := range newItemFieldMap {
		mapper.FieldByName(itemValue, fieldName).Set(newItemFieldMap[fieldName])
	}

	if !inTx {
		// This is only executed if t.Database() was **not** a transaction and if
		// sess was created with sess.NewTransaction().
		return tx.Commit()
	}
	return err

cancel:
	// This goto label should only be used when we got an error within a
	// transaction and we don't want to continue.

	if !inTx {
		// This is only executed if t.Database() was **not** a transaction and if
		// sess was created with sess.NewTransaction().
		tx.Rollback()
	}
	return err
}

// Truncate deletes all rows from the table.
func (c *collection) Truncate() error {
	stmt := exql.Statement{
		Type:  exql.Truncate,
		Table: exql.TableWithName(c.Name()),
	}
	if _, err := c.Database().Exec(&stmt); err != nil {
		return err
	}
	return nil
}

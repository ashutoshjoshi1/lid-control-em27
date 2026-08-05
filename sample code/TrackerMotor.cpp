#include "TrackerMotor.h"

int TrackerMotor::maxAddress = 0;

bool TrackerMotor::readAllValues()
{
	if (!trackerDriver::connected)
	{
		this->realValues = false;
		return false;
	}

	trackerDriver::readRegisters(this->address, 0x00cc, 2);         //request feedback position data from register address 0x00cc
	this->currentPosition = (int32_t)trackerDriver::registerData[0];

	trackerDriver::readRegisters(this->address, 0x00F8, 4);         //request data driver and motor temp data starting at 0x00f8
	this->controllerTemperature = trackerDriver::registerData[0] / 10;
	this->motorTemperature = trackerDriver::registerData[1] / 10;

	trackerDriver::readRegisters(this->address, 0x0080, 2);            //request alarm code from register 0x0080
	this->alarmCode = trackerDriver::registerData[0];

	trackerDriver::readRegisters(this->address, 0x02c4, 6);
	this->homeAcceleration = trackerDriver::registerData[0];
	this->homeStartingSpeed = trackerDriver::registerData[1];
	this->homeSpeed = trackerDriver::registerData[2];
	trackerDriver::readRegisters(this->address, 0x038c, 2);
	this->homePosition = (int32_t)trackerDriver::registerData[0];

	trackerDriver::readRegisters(this->address, 0x0380, 4);
	this->gearN = trackerDriver::registerData[0];
	this->gearD = trackerDriver::registerData[1];

	trackerDriver::readRegisters(this->address, 0x1804, 6);
	this->speed = trackerDriver::registerData[0];
	this->acceleration = trackerDriver::registerData[1];
	this->deceleration = trackerDriver::registerData[2];

	trackerDriver::readRegisters(this->address, 0x0392, 4);
	this->wrapRange = trackerDriver::registerData[0];
	this->wrapOffset = trackerDriver::registerData[1];

	this->realValues = true;
	return true;
}

bool TrackerMotor::writeAllValues()
{
	if (!trackerDriver::connected)
	{
		return false;
	}

	char data[128];



	trackerDriver::insertIntoBuffer(this->homeAcceleration, data, 0);
	trackerDriver::insertIntoBuffer(this->homeStartingSpeed, data, 4);
	trackerDriver::insertIntoBuffer(this->homeSpeed, data, 8);
	trackerDriver::writeMultipleRegisters(this->address, 0x02c4, 6, data);

	trackerDriver::insertIntoBuffer(this->homePosition, data, 0);
	trackerDriver::writeMultipleRegisters(this->address, 0x038c, 2, data);

	trackerDriver::insertIntoBuffer(this->gearN, data, 0);
	trackerDriver::insertIntoBuffer(this->gearD, data, 4);
	trackerDriver::writeMultipleRegisters(this->address, 0x0380, 4, data);

	trackerDriver::insertIntoBuffer(this->speed, data, 0);
	trackerDriver::insertIntoBuffer(this->acceleration, data, 4);
	trackerDriver::insertIntoBuffer(this->deceleration, data, 8);
	trackerDriver::writeMultipleRegisters(this->address, 0x1804, 6, data);

	trackerDriver::insertIntoBuffer(this->wrapRange, data, 0);
	trackerDriver::insertIntoBuffer(this->wrapOffset, data, 4);
	trackerDriver::writeMultipleRegisters(this->address, 0x0392, 4, data);

	return true;
}

TrackerMotor::TrackerMotor()
{
	this->address = ++maxAddress;
}

bool TrackerMotor::readCurrentPosition()
{
	char data[4];
	
	trackerDriver::readRegisters(this->address, 0x00cc, 2);         //request feedback position data from register address 0x00cc
	this->currentPosition = (int32_t)trackerDriver::registerData[0];

	return true;
}

bool TrackerMotor::writeHomePosition()
{
	char data[16];
	
	trackerDriver::insertIntoBuffer(this->homePosition, data, 0);
	trackerDriver::writeMultipleRegisters(this->address, 0x038c, 2, data);

	return true;
}
